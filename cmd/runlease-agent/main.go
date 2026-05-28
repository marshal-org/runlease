// runlease-agent runs on each launched instance. It connects outbound to the
// relay over WebSocket, registers its token, then runs a yamux server on the
// WebSocket — accepting one stream per incoming CLI client and proxying each to
// localhost:22 (sshd). On disconnect it reconnects automatically.
//
// Required env vars (written to /etc/runlease-agent.env by user-data):
//   RUNLEASE_RELAY_ADDR  – relay address: host:port or ws[s]://host[:port]
//   RUNLEASE_RELAY_TOKEN – per-instance secret pre-registered by the CLI
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"

	"github.com/runlease/runlease/pkg/relay"
)

func main() {
	relayAddr := os.Getenv("RUNLEASE_RELAY_ADDR")
	token := os.Getenv("RUNLEASE_RELAY_TOKEN")

	if relayAddr == "" || token == "" {
		fmt.Fprintln(os.Stderr, "RUNLEASE_RELAY_ADDR and RUNLEASE_RELAY_TOKEN must be set")
		os.Exit(1)
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	log.Info("runlease-agent starting", "relay", relayAddr)

	for {
		if err := runSession(relayAddr, token, log); err != nil {
			log.Warn("session ended, reconnecting in 5s", "err", err)
		}
		time.Sleep(5 * time.Second)
	}
}

func runSession(relayAddr, token string, log *slog.Logger) error {
	url := relayWSURL(relayAddr)
	ws, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		return fmt.Errorf("dial relay: %w", err)
	}
	wsNet := relay.NewWSConn(ws)
	defer wsNet.Close()

	if err := wsSend(ws, map[string]string{"role": "instance", "token": token}); err != nil {
		return fmt.Errorf("send handshake: %w", err)
	}
	resp, err := wsRecv(ws)
	if err != nil {
		return fmt.Errorf("read registered: %w", err)
	}
	if resp["status"] != "registered" {
		return fmt.Errorf("registration rejected: %s %s", resp["status"], resp["message"])
	}
	log.Info("registered with relay, starting yamux server")

	// The WebSocket carries yamux frames from here on. Relay runs yamux client,
	// agent runs yamux server — symmetric API, but only the client opens streams.
	cfg := yamux.DefaultConfig()
	// Keep yamux keepalives enabled — WebSocket on its own doesn't ping, so
	// NAT timeouts can silently drop an idle outbound session.
	cfg.StreamOpenTimeout = 30 * time.Second
	cfg.LogOutput = io.Discard
	session, err := yamux.Server(wsNet, cfg)
	if err != nil {
		return fmt.Errorf("yamux.Server: %w", err)
	}
	defer session.Close()

	for {
		stream, err := session.AcceptStream()
		if err != nil {
			return fmt.Errorf("yamux accept: %w", err)
		}
		go proxyStreamToSSHD(stream, log)
	}
}

// proxyStreamToSSHD dials localhost:22 and bidirectionally copies bytes between
// the yamux stream and sshd. One stream = one independent SSH session for the
// CLI on the other end.
func proxyStreamToSSHD(stream *yamux.Stream, log *slog.Logger) {
	defer stream.Close()
	local, err := net.DialTimeout("tcp", "localhost:22", 5*time.Second)
	if err != nil {
		log.Warn("dial local sshd failed", "err", err)
		return
	}
	defer local.Close()
	done := make(chan struct{}, 2)
	go func() { io.Copy(local, stream); done <- struct{}{} }() //nolint:errcheck
	go func() { io.Copy(stream, local); done <- struct{}{} }() //nolint:errcheck
	<-done
}

func wsSend(ws *websocket.Conn, m map[string]string) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return ws.WriteMessage(websocket.BinaryMessage, append(data, '\n'))
}

func wsRecv(ws *websocket.Conn) (map[string]string, error) {
	ws.SetReadDeadline(time.Now().Add(60 * time.Second)) //nolint:errcheck
	defer ws.SetReadDeadline(time.Time{})                //nolint:errcheck
	_, data, err := ws.ReadMessage()
	if err != nil {
		return nil, err
	}
	data = []byte(strings.TrimRight(string(data), "\n\r"))
	var m map[string]string
	return m, json.Unmarshal(data, &m)
}

// relayWSURL converts a relay addr (host:port or ws[s]://...) to a ws[s]://
// URL pointing at /relay.
func relayWSURL(addr string) string {
	if strings.HasPrefix(addr, "ws://") || strings.HasPrefix(addr, "wss://") {
		if !strings.Contains(addr[6:], "/") {
			return addr + "/relay"
		}
		return addr
	}
	return "ws://" + addr + "/relay"
}
