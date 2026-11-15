package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/marshal-org/runlease/pkg/agent"
	"github.com/marshal-org/runlease/pkg/registry"
	"github.com/marshal-org/runlease/pkg/relay"
)

// relayStub answers /ready with a fixed status and /agent with a fixed body.
func relayStub(t *testing.T, readyStatus int, agent string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ready":
			w.WriteHeader(readyStatus)
		case "/agent":
			_, _ = w.Write([]byte(agent))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return "ws://" + strings.TrimPrefix(srv.URL, "http://")
}

func TestWaitForAgent(t *testing.T) {
	t.Run("ready", func(t *testing.T) {
		err := waitForAgent(context.Background(), &bytes.Buffer{}, agentWait{relayAddr: relayStub(t, 200, ""), name: "dev"})
		if err != nil {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("slot unknown to the relay ends the wait at once", func(t *testing.T) {
		start := time.Now()
		err := waitForAgent(context.Background(), &bytes.Buffer{}, agentWait{relayAddr: relayStub(t, 404, ""), name: "dev"})
		if !errors.Is(err, errSlotGone) {
			t.Fatalf("err = %v, want errSlotGone", err)
		}
		if time.Since(start) > 2*time.Second {
			t.Error("a missing slot should not be waited on")
		}
	})
	t.Run("never ready: gives up at the timeout and says why", func(t *testing.T) {
		err := waitForAgent(context.Background(), &bytes.Buffer{}, agentWait{
			relayAddr: relayStub(t, 202, ""), name: "dev", timeout: time.Millisecond,
		})
		if err == nil || !strings.Contains(err.Error(), "did not reach the relay") {
			t.Fatalf("err = %v, want a timeout that names the cause", err)
		}
	})
	t.Run("stopped by the user", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := waitForAgent(ctx, &bytes.Buffer{}, agentWait{relayAddr: relayStub(t, 202, ""), name: "dev"})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	})
}

func TestWaitForAgent_reportsBootFailure(t *testing.T) {
	orig := sshPollIntervalForTest(time.Millisecond)
	defer orig()
	var asked atomic.Int32
	err := waitForAgent(context.Background(), &bytes.Buffer{}, agentWait{
		relayAddr: relayStub(t, 202, ""), name: "dev", timeout: time.Minute,
		bootFailure: func(context.Context) string {
			asked.Add(1)
			return "it could not download the agent (exit 6)"
		},
	})
	if err == nil || !strings.Contains(err.Error(), "boot script failed") || !strings.Contains(err.Error(), "runlease down dev") {
		t.Fatalf("err = %v, want the boot failure and how to remove the machine", err)
	}
	if asked.Load() == 0 {
		t.Error("the console was never read")
	}
}

func TestExplainBootFailure(t *testing.T) {
	console := "boot ok\n[   12.3] RUNLEASE_BOOT_FAILED stage=agent-verify exit=1\nmore\n"
	got := explainBootFailure(console)
	if !strings.Contains(got, "does not match the checksum") || !strings.Contains(got, "exit 1") {
		t.Errorf("got %q", got)
	}
	if explainBootFailure("all good") != "" {
		t.Error("a console with no failure line must report nothing")
	}
	if got := explainBootFailure("RUNLEASE_BOOT_FAILED stage=novel exit=3"); !strings.Contains(got, "novel") {
		t.Errorf("an unknown stage should still be named, got %q", got)
	}
}

func TestCheckRelayAgent(t *testing.T) {
	const agent = "agent-binary-bytes"
	sum := sha256.Sum256([]byte(agent))
	good := hex.EncodeToString(sum[:])
	addr := relayStub(t, 200, agent)

	if err := checkRelayAgent(context.Background(), &bytes.Buffer{}, addr, good); err != nil {
		t.Errorf("matching digest refused: %v", err)
	}
	err := checkRelayAgent(context.Background(), &bytes.Buffer{}, addr, strings.Repeat("0", 64))
	if err == nil || !strings.Contains(err.Error(), "different releases") {
		t.Errorf("mismatch = %v, want a refusal before launch", err)
	}
	// No digest to check against (a source build): nothing to compare.
	if err := checkRelayAgent(context.Background(), &bytes.Buffer{}, addr, ""); err != nil {
		t.Errorf("empty digest: %v", err)
	}
	// A relay that cannot be asked does not block the launch.
	var note bytes.Buffer
	if err := checkRelayAgent(context.Background(), &note, "ws://127.0.0.1:1", good); err != nil || note.Len() == 0 {
		t.Errorf("unreachable relay: err=%v note=%q, want a note and no error", err, note.String())
	}
}

// sshPollIntervalForTest shortens the poll interval and returns the undo.
func sshPollIntervalForTest(d time.Duration) func() {
	old := sshPollInterval
	sshPollInterval = d
	return func() { sshPollInterval = old }
}

// A failed setup step must end the tail, not leave it waiting for a done marker.
func TestMarkerWriters_seeFailureMarker(t *testing.T) {
	doneCh := make(chan struct{}, 1)
	failedCh := make(chan struct{}, 1)
	var out bytes.Buffer
	done := &markerWriter{out: &out, marker: []byte(setupDoneMarker), signal: doneCh}
	w := &markerWriter{out: done, marker: []byte(setupFailedMarker), signal: failedCh}
	_, _ = w.Write([]byte("installing...\n__RUNLEASE_SETUP_FAIL"))
	_, _ = w.Write([]byte("ED__ step 01 (deps)\n"))
	select {
	case <-failedCh:
	default:
		t.Fatal("failure marker not seen")
	}
	select {
	case <-doneCh:
		t.Fatal("done marker reported for a failed setup")
	default:
	}
	if !strings.Contains(out.String(), "step 01 (deps)") {
		t.Error("the log line naming the failed step was not passed through")
	}
}

// A relay that has lost a machine's slot (it restarted, or the slot passed its
// lifetime) answers "unauthorized". Every command must then register the slot
// again and get through, not only 'connect'. This runs a real relay and a real
// agent in-process.
func TestOpenRelay_reRegistersLostSlot(t *testing.T) {
	ts := httptest.NewServer(relay.New(slog.New(slog.NewTextHandler(io.Discard, nil)), relay.Config{}))
	t.Cleanup(ts.Close)
	addr := "ws://" + strings.TrimPrefix(ts.URL, "http://")

	// Stands in for sshd on the machine.
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
			_, _ = c.Write([]byte("hello from the machine"))
			c.Close()
		}
	}()

	const token = "lost-slot-0123456789abcdef0123456789abcdef"
	// The machine's agent: refused until the slot exists, then it gets in.
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() {
		for {
			_ = agent.RunOnce(agent.Config{RelayAddr: addr, Token: token, LocalTarget: ln.Addr().String(),
				Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	}()

	registered := 0
	orig := registerSlot
	t.Cleanup(func() { registerSlot = orig })
	registerSlot = func(_ context.Context, relayAddr, tok string) error {
		registered++
		ws, _, err := websocket.DefaultDialer.Dial(relayWSURL(relayAddr), nil)
		if err != nil {
			return err
		}
		defer ws.Close()
		msg, _ := json.Marshal(map[string]string{"role": "register", "token": tok})
		if err := ws.WriteMessage(websocket.BinaryMessage, append(msg, '\n')); err != nil {
			return err
		}
		_, _, err = ws.ReadMessage()
		return err
	}

	var note bytes.Buffer
	conn, err := openRelay(context.Background(), &note, &registry.Entry{RelayAddr: addr, RelayToken: token})
	if err != nil {
		t.Fatalf("openRelay: %v", err)
	}
	defer conn.Close()
	if registered != 1 {
		t.Errorf("slot registered %d times, want once", registered)
	}
	if !strings.Contains(note.String(), "Registering it again") {
		t.Errorf("the user was not told: %q", note.String())
	}
	buf := make([]byte, 64)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, _ := conn.Read(buf)
	if got := string(buf[:n]); got != "hello from the machine" {
		t.Errorf("read %q through the relay, want the machine's greeting", got)
	}
}
