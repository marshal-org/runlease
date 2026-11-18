package relay

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func digestOf(b string) string {
	sum := sha256.Sum256([]byte(b))
	return hex.EncodeToString(sum[:])
}

// releaseStub serves release agents at /download/<version>/agent and counts
// the downloads.
func releaseStub(t *testing.T, agents map[string]string) (url string, hits *atomic.Int32) {
	t.Helper()
	hits = &atomic.Int32{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		v := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/download/"), "/agent")
		body, ok := agents[v]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)
	return ts.URL + "/download/{version}/agent", hits
}

func startAgentServer(t *testing.T, cfg Config) string {
	t.Helper()
	ts := httptest.NewServer(New(slog.New(slog.NewTextHandler(io.Discard, nil)), cfg))
	t.Cleanup(ts.Close)
	return ts.URL
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := httpClient.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestAgentByDigest(t *testing.T) {
	const own, old, bad = "agent v0.10.0", "agent v0.9.0", "agent v0.8.0"
	ownPath := filepath.Join(t.TempDir(), "runlease-agent")
	if err := os.WriteFile(ownPath, []byte(own), 0o644); err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	// v0.8.0 on the release server is not the binary the CLI asks for.
	relURL, hits := releaseStub(t, map[string]string{"v0.9.0": old, "v0.8.0": "tampered"})
	srv := startAgentServer(t, Config{AgentPath: ownPath, AgentCacheDir: cache, AgentReleaseURL: relURL})

	t.Run("the relay's own agent", func(t *testing.T) {
		if code, body := get(t, srv+"/agent/sha256/"+digestOf(own)); code != 200 || body != own {
			t.Errorf("got %d %q", code, body)
		}
		if code, body := get(t, srv+"/agent"); code != 200 || body != own {
			t.Errorf("/agent: got %d %q", code, body)
		}
	})

	t.Run("an older release is fetched once, then served from the cache", func(t *testing.T) {
		before := hits.Load()
		for range 3 {
			if code, body := get(t, srv+"/agent/sha256/"+digestOf(old)+"?version=v0.9.0"); code != 200 || body != old {
				t.Fatalf("got %d %q", code, body)
			}
		}
		if n := hits.Load() - before; n != 1 {
			t.Errorf("release downloads = %d, want 1", n)
		}
		if b, err := os.ReadFile(filepath.Join(cache, digestOf(old))); err != nil || string(b) != old {
			t.Errorf("cache file: %q %v", b, err)
		}
	})

	t.Run("a release whose agent has another digest is refused and not kept", func(t *testing.T) {
		before := hits.Load()
		for range 2 {
			if code, _ := get(t, srv+"/agent/sha256/"+digestOf(bad)+"?version=v0.8.0"); code != 404 {
				t.Errorf("got %d, want 404", code)
			}
		}
		if n := hits.Load() - before; n != 1 {
			t.Errorf("release downloads = %d, want 1 (a miss is remembered)", n)
		}
		if _, err := os.Stat(filepath.Join(cache, digestOf(bad))); err == nil {
			t.Error("a binary with the wrong digest was kept")
		}
	})

	t.Run("an unknown digest with no version is not fetched", func(t *testing.T) {
		before := hits.Load()
		if code, _ := get(t, srv+"/agent/sha256/"+digestOf("x")); code != 404 {
			t.Errorf("got %d, want 404", code)
		}
		if hits.Load() != before {
			t.Error("fetched without a version")
		}
	})

	t.Run("bad input", func(t *testing.T) {
		for _, p := range []string{
			"/agent/sha256/xyz",
			"/agent/sha256/" + strings.ToUpper(digestOf(own)),
			"/agent/sha256/" + digestOf(own) + "?version=../../evil",
			"/agent/sha256/" + digestOf(own) + "?version=v1.2",
		} {
			if code, _ := get(t, srv+p); code != 400 {
				t.Errorf("%s: got %d, want 400", p, code)
			}
		}
	})

	t.Run("a restarted relay keeps what it fetched", func(t *testing.T) {
		srv2 := startAgentServer(t, Config{AgentCacheDir: cache})
		if code, body := get(t, srv2+"/agent/sha256/"+digestOf(old)); code != 200 || body != old {
			t.Errorf("got %d %q", code, body)
		}
	})

	t.Run("discovery says the relay serves agents by digest", func(t *testing.T) {
		_, body := get(t, srv+"/.well-known/runlease-relay")
		var doc struct {
			AgentByDigest bool           `json:"agentByDigest"`
			Protocol      map[string]int `json:"protocol"`
		}
		if err := json.Unmarshal([]byte(body), &doc); err != nil || !doc.AgentByDigest || doc.Protocol["max"] != MaxProtocol {
			t.Errorf("doc = %s (%v)", body, err)
		}
	})
}

func TestAgentByDigest_notConfigured(t *testing.T) {
	srv := startServer(t)
	if code, _ := get(t, srv+"/agent/sha256/"+digestOf("x")); code != http.StatusServiceUnavailable {
		t.Errorf("got %d, want 503", code)
	}
}

func TestRelay_protocolRange(t *testing.T) {
	srv := startServer(t)
	const tok = "proto-token-0123456789abcdef0123456789abcdef"
	for _, p := range []string{"", ProtocolString} {
		c := dialWS(t, srv)
		sendWS(t, c, map[string]string{"role": "register", "token": tok + p, "proto": p})
		if r := recvWS(t, c); r["status"] != "ok" {
			t.Errorf("proto %q: got %v, want ok", p, r)
		}
	}
	for _, p := range []string{"99", "0", "one"} {
		c := dialWS(t, srv)
		sendWS(t, c, map[string]string{"role": "register", "token": tok + "x", "proto": p})
		if r := recvWS(t, c); r["status"] != "error" || !strings.Contains(r["message"], "Upgrade runlease") {
			t.Errorf("proto %q: got %v, want a refusal that says to upgrade", p, r)
		}
	}
}
