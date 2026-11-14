package relay

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
)

var httpClient = &http.Client{Timeout: 5 * time.Second}

// ── helpers ───────────────────────────────────────────────────────────────────

func startServer(t *testing.T) string {
	t.Helper()
	s := New(slog.New(slog.NewTextHandler(io.Discard, nil)), Config{})
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	return ts.URL // "http://127.0.0.1:PORT"
}

func wsURL(serverURL string) string {
	return "ws" + strings.TrimPrefix(serverURL, "http") + "/relay"
}

func dialWS(t *testing.T, serverURL string) *websocket.Conn {
	t.Helper()
	c, _, err := websocket.DefaultDialer.Dial(wsURL(serverURL), nil)
	if err != nil {
		t.Fatalf("dial WS: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func sendWS(t *testing.T, c *websocket.Conn, m map[string]string) {
	t.Helper()
	data, _ := json.Marshal(m)
	if err := c.WriteMessage(websocket.BinaryMessage, append(data, '\n')); err != nil {
		t.Fatalf("sendWS: %v", err)
	}
}

func recvWS(t *testing.T, c *websocket.Conn) map[string]string {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(5 * time.Second)) //nolint:errcheck
	defer c.SetReadDeadline(time.Time{})               //nolint:errcheck
	_, data, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("recvWS: %v", err)
	}
	data = []byte(strings.TrimRight(string(data), "\n\r"))
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("recvWS unmarshal: %v (raw: %q)", err, data)
	}
	return m
}

func registerToken(t *testing.T, serverURL, token string) {
	t.Helper()
	c := dialWS(t, serverURL)
	sendWS(t, c, map[string]string{"role": "register", "token": token})
	if r := recvWS(t, c); r["status"] != "ok" {
		t.Fatalf("register: want ok, got %v", r)
	}
}

// fakeAgent connects as role=instance, completes the registered handshake,
// then runs a yamux server that echoes whatever bytes each stream sends.
// Returns a stop function the test should defer.
func fakeAgent(t *testing.T, serverURL, token string, onAccept func(stream *yamux.Stream)) func() {
	t.Helper()
	ws, _, err := websocket.DefaultDialer.Dial(wsURL(serverURL), nil)
	if err != nil {
		t.Fatalf("agent dial: %v", err)
	}
	sendWS(t, ws, map[string]string{"role": "instance", "token": token})
	if r := recvWS(t, ws); r["status"] != "registered" {
		t.Fatalf("agent register: want registered, got %v", r)
	}
	wsNet := NewWSConn(ws)
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	session, err := yamux.Server(wsNet, cfg)
	if err != nil {
		t.Fatalf("agent yamux.Server: %v", err)
	}
	stopCh := make(chan struct{})
	go func() {
		for {
			stream, err := session.AcceptStream()
			if err != nil {
				return
			}
			go onAccept(stream)
		}
	}()
	return func() {
		close(stopCh)
		session.Close()
		ws.Close()
	}
}

// echoStream reads bytes from the stream and writes them back. Used as the
// onAccept handler in tests that want a simple loopback "sshd" replacement.
func echoStream(stream *yamux.Stream) {
	defer stream.Close()
	io.Copy(stream, stream) //nolint:errcheck
}

// connectClient performs the client handshake and returns the underlying WS.
func connectClient(t *testing.T, serverURL, token string) *websocket.Conn {
	t.Helper()
	ws, _, err := websocket.DefaultDialer.Dial(wsURL(serverURL), nil)
	if err != nil {
		t.Fatalf("client dial: %v", err)
	}
	t.Cleanup(func() { ws.Close() })
	sendWS(t, ws, map[string]string{"role": "client", "token": token})
	if r := recvWS(t, ws); r["status"] != "connected" {
		t.Fatalf("client connected: want connected, got %v", r)
	}
	return ws
}

// ── tests ─────────────────────────────────────────────────────────────────────

// TestRelay_singleClientRoundtrip: agent registers, one client connects, bytes
// echo through.
func TestRelay_singleClientRoundtrip(t *testing.T) {
	srv := startServer(t)
	const tok = "test-token-1-0123456789abcdef0123456789abcdef"
	registerToken(t, srv, tok)
	stop := fakeAgent(t, srv, tok, echoStream)
	defer stop()

	cli := connectClient(t, srv, tok)
	payload := []byte("hello relay")
	if err := cli.WriteMessage(websocket.BinaryMessage, payload); err != nil {
		t.Fatalf("client write: %v", err)
	}
	cli.SetReadDeadline(time.Now().Add(3 * time.Second)) //nolint:errcheck
	_, got, err := cli.ReadMessage()
	if err != nil {
		t.Fatalf("client read: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("got %q, want %q", got, payload)
	}
}

// TestRelay_concurrentClients verifies the central yamux promise — multiple
// CLI clients can connect to the same slot concurrently, each gets its own
// independent stream, and bytes don't get crossed between streams.
func TestRelay_concurrentClients(t *testing.T) {
	srv := startServer(t)
	const tok = "test-token-concurrent-0123456789abcdef0123456789abcdef"
	registerToken(t, srv, tok)
	stop := fakeAgent(t, srv, tok, echoStream)
	defer stop()

	const N = 4
	var wg sync.WaitGroup
	errs := make(chan error, N)
	for i := 0; i < N; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			cli := connectClient(t, srv, tok)
			payload := []byte(fmt.Sprintf("client-%d", i))
			if err := cli.WriteMessage(websocket.BinaryMessage, payload); err != nil {
				errs <- fmt.Errorf("write %d: %w", i, err)
				return
			}
			cli.SetReadDeadline(time.Now().Add(3 * time.Second)) //nolint:errcheck
			_, got, err := cli.ReadMessage()
			if err != nil {
				errs <- fmt.Errorf("read %d: %w", i, err)
				return
			}
			if string(got) != string(payload) {
				errs <- fmt.Errorf("client %d: got %q, want %q", i, got, payload)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

// TestRelay_clientArriveFirst: the relay accepts a client that connects before
// the agent — the client blocks in handleClient until the agent shows up and
// the coordinator opens a yamux stream for it.
func TestRelay_clientArriveFirst(t *testing.T) {
	srv := startServer(t)
	const tok = "test-token-early-0123456789abcdef0123456789abcdef"
	registerToken(t, srv, tok)

	done := make(chan error, 1)
	go func() {
		ws, _, err := websocket.DefaultDialer.Dial(wsURL(srv), nil)
		if err != nil {
			done <- err
			return
		}
		defer ws.Close()
		sendWS(t, ws, map[string]string{"role": "client", "token": tok})
		r := recvWS(t, ws)
		if r["status"] != "connected" {
			done <- fmt.Errorf("client status: %v", r)
			return
		}
		done <- nil
	}()

	// Small delay so the client lands on clientCh first.
	time.Sleep(50 * time.Millisecond)
	stop := fakeAgent(t, srv, tok, echoStream)
	defer stop()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("client did not receive connected after agent arrived")
	}
}

// TestRelay_unregisteredTokenRejected: relay rejects tokens never registered.
func TestRelay_unregisteredTokenRejected(t *testing.T) {
	srv := startServer(t)
	for _, role := range []string{"instance", "client"} {
		c := dialWS(t, srv)
		sendWS(t, c, map[string]string{"role": role, "token": "ghost-token-0123456789abcdef0123456789abcdef"})
		r := recvWS(t, c)
		c.Close()
		if r["status"] != "unauthorized" {
			t.Errorf("role %s: want unauthorized, got %v", role, r)
		}
	}
}

// TestRelay_doubleInstanceRejected: second instance with same token gets "busy".
func TestRelay_doubleInstanceRejected(t *testing.T) {
	srv := startServer(t)
	const tok = "test-token-double-0123456789abcdef0123456789abcdef"
	registerToken(t, srv, tok)

	inst1 := dialWS(t, srv)
	sendWS(t, inst1, map[string]string{"role": "instance", "token": tok})
	if r := recvWS(t, inst1); r["status"] != "registered" {
		t.Fatalf("inst1: want registered, got %v", r)
	}

	inst2 := dialWS(t, srv)
	sendWS(t, inst2, map[string]string{"role": "instance", "token": tok})
	if r := recvWS(t, inst2); r["status"] != "busy" {
		t.Errorf("inst2: want busy, got %v", r)
	}
}

// TestRelay_doubleRegisterRejected: re-registering an existing token returns error.
func TestRelay_doubleRegisterRejected(t *testing.T) {
	srv := startServer(t)
	const tok = "test-token-dup-0123456789abcdef0123456789abcdef"
	registerToken(t, srv, tok)

	c := dialWS(t, srv)
	sendWS(t, c, map[string]string{"role": "register", "token": tok})
	if r := recvWS(t, c); r["status"] != "error" {
		t.Errorf("want error for duplicate register, got %v", r)
	}
}

// TestRelay_httpAgentNotConfigured: GET /agent returns 503 with no binary path.
func TestRelay_httpAgentNotConfigured(t *testing.T) {
	srv := startServer(t)
	resp, err := getStatus(srv + "/agent")
	if err != nil {
		t.Fatal(err)
	}
	if resp != 503 {
		t.Errorf("expected 503, got %d", resp)
	}
}

// TestRelay_httpUnknownPath: GET /unknown returns 404.
func TestRelay_httpUnknownPath(t *testing.T) {
	srv := startServer(t)
	resp, err := getStatus(srv + "/unknown")
	if err != nil {
		t.Fatal(err)
	}
	if resp != 404 {
		t.Errorf("expected 404, got %d", resp)
	}
}

// TestRelay_health: GET /health returns 200.
func TestRelay_health(t *testing.T) {
	srv := startServer(t)
	resp, err := getStatus(srv + "/health")
	if err != nil {
		t.Fatal(err)
	}
	if resp != 200 {
		t.Errorf("expected 200, got %d", resp)
	}
}

func getStatus(url string) (int, error) {
	resp, err := httpClient.Get(url)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}

// TestRelay_malformedTokenRejected: a token that is too short, too long or has
// characters outside [A-Za-z0-9_-] is refused for every role, and the relay
// keeps serving afterwards. A short token on "register" used to panic the
// whole process when the relay sliced it for its log line.
func TestRelay_malformedTokenRejected(t *testing.T) {
	srv := startServer(t)
	bad := []string{"abc", "short-token", strings.Repeat("a", 129), strings.Repeat("a", 31) + "\x00", strings.Repeat("a", 40) + " b"}
	for _, role := range []string{"register", "instance", "client"} {
		for _, tok := range bad {
			c := dialWS(t, srv)
			sendWS(t, c, map[string]string{"role": role, "token": tok})
			resp := recvWS(t, c)
			if resp["status"] != "error" {
				t.Errorf("role=%s token=%q: want status error, got %v", role, tok, resp)
			}
			c.Close()
		}
	}
	// The relay must still be alive and usable.
	registerToken(t, srv, "after-malformed-0123456789abcdef0123456789abcdef")
}

// TestRelay_maxSlots: registration is refused once the slot cap is reached.
func TestRelay_maxSlots(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ts := httptest.NewServer(New(log, Config{MaxSlots: 2}))
	t.Cleanup(ts.Close)
	url := ts.URL

	registerToken(t, url, "cap-one-0123456789abcdef0123456789abcdef")
	registerToken(t, url, "cap-two-0123456789abcdef0123456789abcdef")

	c := dialWS(t, url)
	defer c.Close()
	sendWS(t, c, map[string]string{"role": "register", "token": "cap-three-0123456789abcdef0123456789abcdef"})
	resp := recvWS(t, c)
	if resp["status"] != "error" || !strings.Contains(resp["message"], "capacity") {
		t.Fatalf("want capacity error, got %v", resp)
	}
}

func TestValidToken(t *testing.T) {
	good := []string{strings.Repeat("a", 32), strings.Repeat("0f", 32), strings.Repeat("A-_9", 10)}
	for _, tok := range good {
		if !validToken(tok) {
			t.Errorf("validToken(%q) = false, want true", tok)
		}
	}
	for _, tok := range []string{"", "a", strings.Repeat("a", 31), strings.Repeat("a", 129), strings.Repeat("a", 32) + "/"} {
		if validToken(tok) {
			t.Errorf("validToken(%q) = true, want false", tok)
		}
	}
	if got := shortTok("abc"); got != "…" {
		t.Errorf("shortTok on a short token = %q", got)
	}
}
