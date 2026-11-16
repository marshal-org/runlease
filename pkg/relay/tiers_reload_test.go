package relay

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fileTieredServer starts a relay whose limits come from a file, as the real
// binary does, and returns the server, its URL and the file's path.
func fileTieredServer(t *testing.T, yamlText string) (*Server, string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tiers.yaml")
	if err := os.WriteFile(path, []byte(yamlText), 0o644); err != nil {
		t.Fatal(err)
	}
	tiers, err := LoadTiers(path)
	if err != nil {
		t.Fatalf("LoadTiers: %v", err)
	}
	srv := New(slog.New(slog.NewTextHandler(io.Discard, nil)), Config{
		Tiers:   tiers,
		Version: "test",
		Auth:    AuthConfig{Verifier: fakeVerifier, GoogleClientIDs: []string{"client-id.apps.example"}},
	})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return srv, ts.URL, path
}

func TestReloadTiers_newAssignmentAppliesWithoutRestart(t *testing.T) {
	srv, url, path := fileTieredServer(t, strings.Replace(testTiersYAML, "idleTTL: 150ms", "idleTTL: 1h", 1))

	// alice is on the free tier and fills it.
	for i := 0; i < 2; i++ {
		if resp := registerAs(t, url, "alice@free.example", tok(fmt.Sprintf("a-%d", i))); resp["status"] != "ok" {
			t.Fatalf("slot %d: %v", i, resp)
		}
	}
	if resp := registerAs(t, url, "alice@free.example", tok("a-2")); resp["status"] != "limit" {
		t.Fatalf("third free slot: %v", resp)
	}

	// The operator assigns her to the team tier.
	next := strings.Replace(strings.Replace(testTiersYAML, "idleTTL: 150ms", "idleTTL: 1h", 1),
		"    Boss@Example.com: team", "    Boss@Example.com: team\n    alice@free.example: team", 1)
	if err := os.WriteFile(path, []byte(next), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := srv.ReloadTiers(path)
	if err != nil || !changed {
		t.Fatalf("ReloadTiers = %v, %v; want true, nil", changed, err)
	}

	if got := me(t, url, "alice@free.example")["tier"]; got != "team" {
		t.Fatalf("tier after reload = %v, want team", got)
	}
	// The slots she already holds are still hers, and she may now have more.
	if got := used(t, url, "alice@free.example"); got != 2 {
		t.Fatalf("used after reload = %d, want 2: a reload must not drop slots", got)
	}
	if resp := registerAs(t, url, "alice@free.example", tok("a-3")); resp["status"] != "ok" {
		t.Fatalf("slot after reload: %v", resp)
	}

	// The same content again is not a change.
	if changed, err := srv.ReloadTiers(path); err != nil || changed {
		t.Fatalf("second ReloadTiers = %v, %v; want false, nil", changed, err)
	}
}

func TestReloadTiers_badFileKeepsTheLimitsInForce(t *testing.T) {
	srv, url, path := fileTieredServer(t, testTiersYAML)

	for name, bad := range map[string]string{
		"misspelt field": strings.Replace(testTiersYAML, "maxSlots: 2", "maxSlot: 2", 1),
		"undefined tier": strings.Replace(testTiersYAML, "paid.example: team", "paid.example: gold", 1),
		"not yaml":       "default: [",
		"empty":          "",
	} {
		if err := os.WriteFile(path, []byte(bad), 0o644); err != nil {
			t.Fatal(err)
		}
		if changed, err := srv.ReloadTiers(path); err == nil || changed {
			t.Errorf("%s: ReloadTiers = %v, %v; want false and an error", name, changed, err)
		}
		// The same bad content is reported once, not on every poll.
		if changed, err := srv.ReloadTiers(path); err != nil || changed {
			t.Errorf("%s: second ReloadTiers = %v, %v; want false, nil", name, changed, err)
		}
		if got := me(t, url, "dev@paid.example")["tier"]; got != "team" {
			t.Errorf("%s: tier = %v, want team: a bad file must change nothing", name, got)
		}
	}

	// A missing file is an error too, and changes nothing.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if changed, err := srv.ReloadTiers(path); err == nil || changed {
		t.Errorf("missing file: ReloadTiers = %v, %v; want false and an error", changed, err)
	}
	if got := me(t, url, "someone@free.example")["tier"]; got != "free" {
		t.Errorf("missing file: tier = %v, want free", got)
	}

	// A good file afterwards is applied.
	good := strings.Replace(testTiersYAML, "paid.example: team", "paid.example: free", 1)
	if err := os.WriteFile(path, []byte(good), 0o644); err != nil {
		t.Fatal(err)
	}
	if changed, err := srv.ReloadTiers(path); err != nil || !changed {
		t.Fatalf("good file: ReloadTiers = %v, %v; want true, nil", changed, err)
	}
	if got := me(t, url, "dev@paid.example")["tier"]; got != "free" {
		t.Errorf("good file: tier = %v, want free", got)
	}
}

func TestWatchTiers_picksUpAChange(t *testing.T) {
	srv, url, path := fileTieredServer(t, testTiersYAML)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { srv.WatchTiers(ctx, path, 20*time.Millisecond); close(done) }()
	defer func() { cancel(); <-done }()

	// Nothing changed yet: the watcher must not have swapped anything.
	time.Sleep(60 * time.Millisecond)
	if got := me(t, url, "dev@paid.example")["tier"]; got != "team" {
		t.Fatalf("tier before the change = %v, want team", got)
	}

	// Written the way the deploy module writes it: a new file moved into place.
	tmp := path + ".new"
	if err := os.WriteFile(tmp, []byte(strings.Replace(testTiersYAML, "paid.example: team", "paid.example: free", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if me(t, url, "dev@paid.example")["tier"] == "free" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the watcher did not apply the changed file within 3s")
}
