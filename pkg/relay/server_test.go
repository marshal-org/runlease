package relay

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
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

// sendWS sends m as a binary WebSocket frame containing JSON+\n.
func sendWS(t *testing.T, c *websocket.Conn, m map[string]string) {
	t.Helper()
	data, _ := json.Marshal(m)
	if err := c.WriteMessage(websocket.BinaryMessage, append(data, '\n')); err != nil {
		t.Fatalf("sendWS: %v", err)
	}
}

// recvWS reads the next binary frame, strips trailing \n, and decodes JSON.
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

// ── tests ─────────────────────────────────────────────────────────────────────

// TestRelay_fullSession: happy path — instance arrives first, then client.
func TestRelay_fullSession(t *testing.T) {
	srv := startServer(t)
	const tok = "test-token-1"
	registerToken(t, srv, tok)

	// Instance connects.
	inst := dialWS(t, srv)
	sendWS(t, inst, map[string]string{"role": "instance", "token": tok})
	if r := recvWS(t, inst); r["status"] != "registered" {
		t.Fatalf("want registered, got %v", r)
	}

	// Client connects in background (waits for "connected").
	type result struct {
		conn *websocket.Conn
		err  error
	}
	cliCh := make(chan result, 1)
	go func() {
		c := dialWS(t, srv)
		sendWS(t, c, map[string]string{"role": "client", "token": tok})
		m := recvWS(t, c)
		if m["status"] != "connected" {
			c.Close()
			cliCh <- result{err: fmt.Errorf("client: got %v", m)}
			return
		}
		cliCh <- result{conn: c}
	}()

	// Instance receives "paired", acks with "ready".
	inst.SetReadDeadline(time.Now().Add(3 * time.Second)) //nolint:errcheck
	paired := recvWS(t, inst)
	inst.SetReadDeadline(time.Time{}) //nolint:errcheck
	if paired["status"] != "paired" {
		t.Fatalf("want paired, got %v", paired)
	}
	sendWS(t, inst, map[string]string{"status": "ready"})

	res := <-cliCh
	if res.err != nil {
		t.Fatal(res.err)
	}

	// Verify bidirectional bridge.
	payload := []byte("hello relay")
	res.conn.WriteMessage(websocket.BinaryMessage, payload) //nolint:errcheck

	inst.SetReadDeadline(time.Now().Add(3 * time.Second)) //nolint:errcheck
	_, got, err := inst.ReadMessage()
	if err != nil {
		t.Fatalf("read on instance: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("got %q, want %q", got, payload)
	}
}

// TestRelay_clientArriveFirst: relay pairs correctly even when client connects
// before the instance.
func TestRelay_clientArriveFirst(t *testing.T) {
	srv := startServer(t)
	const tok = "test-token-2"
	registerToken(t, srv, tok)

	cli := dialWS(t, srv)
	sendWS(t, cli, map[string]string{"role": "client", "token": tok})

	time.Sleep(50 * time.Millisecond)

	inst := dialWS(t, srv)
	sendWS(t, inst, map[string]string{"role": "instance", "token": tok})
	if r := recvWS(t, inst); r["status"] != "registered" {
		t.Fatalf("want registered, got %v", r)
	}

	inst.SetReadDeadline(time.Now().Add(3 * time.Second)) //nolint:errcheck
	paired := recvWS(t, inst)
	inst.SetReadDeadline(time.Time{}) //nolint:errcheck
	if paired["status"] != "paired" {
		t.Fatalf("want paired, got %v", paired)
	}
	sendWS(t, inst, map[string]string{"status": "ready"})

	cli.SetReadDeadline(time.Now().Add(3 * time.Second)) //nolint:errcheck
	m := recvWS(t, cli)
	if m["status"] != "connected" {
		t.Fatalf("client: want connected, got %v", m)
	}
}

// TestRelay_unregisteredTokenRejected: relay rejects tokens never registered.
func TestRelay_unregisteredTokenRejected(t *testing.T) {
	srv := startServer(t)
	for _, role := range []string{"instance", "client"} {
		c := dialWS(t, srv)
		sendWS(t, c, map[string]string{"role": role, "token": "ghost-token"})
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
	const tok = "test-token-double"
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
	const tok = "test-token-dup"
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
