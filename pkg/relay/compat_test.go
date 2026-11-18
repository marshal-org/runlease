package relay

import (
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestRelay_releasedAgent runs a real agent binary, built by an earlier
// release, against this relay. A relay release must not break the agents of
// the CLIs already in use. CI downloads the previous release's agent and sets
// RUNLEASE_COMPAT_AGENT; without it the test is skipped.
func TestRelay_releasedAgent(t *testing.T) {
	bin := os.Getenv("RUNLEASE_COMPAT_AGENT")
	if bin == "" {
		t.Skip("RUNLEASE_COMPAT_AGENT is not set")
	}
	srv := startServer(t)
	const tok = "compat-token-0123456789abcdef0123456789abcdef"
	registerToken(t, srv, tok)

	// A loopback "sshd" for the agent to bridge each stream to.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); buf := make([]byte, 4096); n, _ := c.Read(buf); c.Write(buf[:n]) }() //nolint:errcheck
		}
	}()

	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"RUNLEASE_RELAY_ADDR=ws://"+strings.TrimPrefix(srv, "http://"),
		"RUNLEASE_RELAY_TOKEN="+tok,
		"RUNLEASE_LOCAL_TARGET="+ln.Addr().String(),
	)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start agent: %v", err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() }) //nolint:errcheck

	deadline := time.Now().Add(20 * time.Second)
	for {
		req, _ := http.NewRequest("GET", srv+"/ready", nil)
		req.Header.Set("X-Relay-Token", tok)
		resp, err := httpClient.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the released agent never became ready on this relay")
		}
		time.Sleep(200 * time.Millisecond)
	}

	cli := connectClient(t, srv, tok)
	if err := cli.WriteMessage(websocket.BinaryMessage, []byte("ping")); err != nil {
		t.Fatal(err)
	}
	cli.SetReadDeadline(time.Now().Add(5 * time.Second)) //nolint:errcheck
	_, got, err := cli.ReadMessage()
	if err != nil || string(got) != "ping" {
		t.Fatalf("echo through the released agent: %q %v", got, err)
	}
}
