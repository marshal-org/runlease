package relay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultAgentReleaseURL is where the relay finds the agent of a release it
// does not hold yet. {version} is replaced by a release tag such as v0.9.0.
const DefaultAgentReleaseURL = "https://github.com/marshal-org/runlease/releases/download/{version}/runlease-agent-linux-amd64"

const (
	// maxAgentSize bounds a download from the release URL.
	maxAgentSize = 128 << 20
	// agentMissTTL is how long a digest that could not be found is not asked
	// for again, so a stream of requests for a bad digest costs one download.
	agentMissTTL = time.Minute
)

var (
	sha256Hex      = regexp.MustCompile(`^[0-9a-f]{64}$`)
	releaseVersion = regexp.MustCompile(`^v([0-9]{1,6})\.([0-9]{1,6})\.([0-9]{1,6})$`)
)

// agentStore serves agent binaries by their SHA-256. Each CLI release asks for
// the agent it was built with, so a relay release does not break the CLIs of
// earlier releases. The digest is the key, and the boot script checks it again,
// so the relay cannot hand out a binary other than the one the CLI asked for.
//
// Binaries come from two places: the relay's own agent (--agent-binary), and
// the release URL, fetched on the first request for a digest and kept in dir.
type agentStore struct {
	dir        string // where fetched agents are kept, one file per digest
	releaseURL string // "" turns fetching off
	client     *http.Client

	mu       sync.Mutex
	paths    map[string]string        // digest -> file
	misses   map[string]time.Time     // digest -> when the last fetch for it failed
	inflight map[string]chan struct{} // digest -> closed when its fetch ends
}

func newAgentStore(dir, releaseURL, ownAgent string) (*agentStore, error) {
	st := &agentStore{
		dir:        dir,
		releaseURL: releaseURL,
		client:     &http.Client{Timeout: 2 * time.Minute},
		paths:      map[string]string{},
		misses:     map[string]time.Time{},
		inflight:   map[string]chan struct{}{},
	}
	if ownAgent != "" {
		d, err := fileSHA256(ownAgent)
		if err != nil {
			return nil, fmt.Errorf("agent binary: %w", err)
		}
		st.paths[d] = ownAgent
	}
	if dir != "" {
		entries, err := os.ReadDir(dir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("agent cache: %w", err)
		}
		// Files are named by digest, but a name is not proof: hash each one.
		for _, e := range entries {
			if !sha256Hex.MatchString(e.Name()) {
				continue
			}
			p := filepath.Join(dir, e.Name())
			if d, err := fileSHA256(p); err == nil && d == e.Name() {
				st.paths[d] = p
			}
		}
	}
	return st, nil
}

// get returns the file that holds the agent with this digest. version is the
// release to fetch it from when the store does not have it; "" means do not
// fetch. It returns os.ErrNotExist when the agent cannot be found.
func (st *agentStore) get(ctx context.Context, digest, version string) (string, error) {
	for {
		st.mu.Lock()
		if p, ok := st.paths[digest]; ok {
			st.mu.Unlock()
			return p, nil
		}
		if version == "" || st.releaseURL == "" || st.dir == "" {
			st.mu.Unlock()
			return "", os.ErrNotExist
		}
		if t, ok := st.misses[digest+" "+version]; ok && time.Since(t) < agentMissTTL {
			st.mu.Unlock()
			return "", os.ErrNotExist
		}
		if wait, ok := st.inflight[digest]; ok {
			st.mu.Unlock()
			select {
			case <-wait:
				continue // look again: the other fetch may have stored it
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		done := make(chan struct{})
		st.inflight[digest] = done
		st.mu.Unlock()

		p, err := st.fetch(digest, version)

		st.mu.Lock()
		delete(st.inflight, digest)
		close(done)
		if err == nil {
			st.paths[digest] = p
		} else {
			st.misses[digest+" "+version] = time.Now()
		}
		st.mu.Unlock()
		return p, err
	}
}

// fetch downloads the agent of a release and keeps it only when its SHA-256 is
// the one asked for. It runs without the request's context, so a client that
// gives up does not waste a download another one may be waiting on.
func (st *agentStore) fetch(digest, version string) (string, error) {
	url := strings.ReplaceAll(st.releaseURL, "{version}", version)
	resp, err := st.client.Get(url)
	if err != nil {
		return "", fmt.Errorf("fetch agent %s: %w", version, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch agent %s: %s: %w", version, resp.Status, os.ErrNotExist)
	}
	if err := os.MkdirAll(st.dir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(st.dir, "fetch-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // gone after the rename
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, maxAgentSize+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", fmt.Errorf("fetch agent %s: %w", version, err)
	}
	if n > maxAgentSize {
		return "", fmt.Errorf("fetch agent %s: larger than %d bytes", version, maxAgentSize)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != digest {
		return "", fmt.Errorf("fetch agent %s: its SHA-256 (%s) is not the one asked for: %w", version, got, os.ErrNotExist)
	}
	p := filepath.Join(st.dir, digest)
	if err := os.Rename(tmp.Name(), p); err != nil {
		return "", err
	}
	return p, nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// serveAgentByDigest answers GET /agent/sha256/<hex>?version=<tag>.
func (s *Server) serveAgentByDigest(w http.ResponseWriter, r *http.Request) {
	digest := strings.TrimPrefix(r.URL.Path, "/agent/sha256/")
	if !sha256Hex.MatchString(digest) {
		http.Error(w, "want /agent/sha256/<64 lowercase hex characters>", http.StatusBadRequest)
		return
	}
	version, ok := canonicalVersion(r.URL.Query().Get("version"))
	if !ok {
		http.Error(w, "version must be a release tag such as v1.2.3", http.StatusBadRequest)
		return
	}
	if s.agents == nil {
		http.Error(w, "agent binary not configured", http.StatusServiceUnavailable)
		return
	}
	p, err := s.agents.get(r.Context(), digest, version)
	switch {
	case errors.Is(err, os.ErrNotExist):
		http.Error(w, "no agent with that SHA-256 on this relay", http.StatusNotFound)
		return
	case err != nil:
		s.log.Warn("agent by digest: fetch failed", "version", version, "err", err)
		http.Error(w, "agent binary unavailable", http.StatusBadGateway)
		return
	}
	serveAgentFile(w, p)
}

func serveAgentFile(w http.ResponseWriter, path string) {
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, "agent binary unavailable", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		http.Error(w, "agent binary unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="runlease-agent"`)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size()))
	io.Copy(w, f) //nolint:errcheck
}

// canonicalVersion turns a release tag from a request into one built from its
// numbers, so nothing but digits and dots from the request reaches the fetch
// URL or a log line. "" is a valid answer: no version, nothing to fetch.
func canonicalVersion(v string) (string, bool) {
	if v == "" {
		return "", true
	}
	m := releaseVersion.FindStringSubmatch(v)
	if m == nil {
		return "", false
	}
	var n [3]int
	for i := range n {
		x, err := strconv.Atoi(m[i+1])
		if err != nil {
			return "", false
		}
		n[i] = x
	}
	return fmt.Sprintf("v%d.%d.%d", n[0], n[1], n[2]), true
}
