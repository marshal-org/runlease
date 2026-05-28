// Package agent holds the runlease-agent core loop, factored out of the main
// binary so it can be exercised by integration tests.
package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"

	"github.com/runlease/runlease/pkg/relay"
)

// Config controls a single agent's behaviour.
type Config struct {
	RelayAddr   string // ws[s]://host[:port] or host:port
	Token       string // pre-registered relay token
	LocalTarget string // tcp dial target for each stream; defaults to "localhost:22"
	Logger      *slog.Logger
}

// RunOnce executes one full agent session: dial the relay, register, then
// serve yamux streams until the relay disconnects or an error occurs.
// Returns the error that ended the session; callers (the binary) wrap this in
// a reconnect loop.
func RunOnce(cfg Config) error {
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	target := cfg.LocalTarget
	if target == "" {
		target = "localhost:22"
	}

	url := relayWSURL(cfg.RelayAddr)
	ws, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		return fmt.Errorf("dial relay: %w", err)
	}
	wsNet := relay.NewWSConn(ws)
	defer wsNet.Close()

	if err := wsSend(ws, map[string]string{"role": "instance", "token": cfg.Token}); err != nil {
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

	yamuxCfg := yamux.DefaultConfig()
	yamuxCfg.StreamOpenTimeout = 30 * time.Second
	yamuxCfg.LogOutput = io.Discard
	session, err := yamux.Server(wsNet, yamuxCfg)
	if err != nil {
		return fmt.Errorf("yamux.Server: %w", err)
	}
	defer session.Close()

	for {
		stream, err := session.AcceptStream()
		if err != nil {
			return fmt.Errorf("yamux accept: %w", err)
		}
		go proxyStream(stream, target, log)
	}
}

// proxyStream dials target and bidirectionally copies bytes between the
// yamux stream and the local TCP connection. One stream = one independent
// client session on the CLI side.
func proxyStream(stream *yamux.Stream, target string, log *slog.Logger) {
	defer stream.Close()
	local, err := net.DialTimeout("tcp", target, 5*time.Second)
	if err != nil {
		log.Warn("dial local target failed", "target", target, "err", err)
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
