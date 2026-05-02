// runlease-agent runs on each launched instance. It connects outbound to the
// relay server over WebSocket and proxies SSH traffic once a CLI client pairs.
// After each session it reconnects automatically.
//
// Required env vars (written to /etc/runlease-agent.env by user-data):
//   RUNLEASE_RELAY_ADDR  – relay address: host:port or ws[s]://host[:port]
//   RUNLEASE_RELAY_TOKEN – per-instance secret pre-registered by the CLI
package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
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
	defer ws.Close()

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
	log.Info("registered with relay, waiting for client")

	resp, err = wsRecvDeadline(ws, 2*time.Hour)
	if err != nil {
		return fmt.Errorf("waiting for paired: %w", err)
	}
	if resp["status"] != "paired" {
		return fmt.Errorf("unexpected relay message: %s", resp["status"])
	}

	local, err := net.DialTimeout("tcp", "localhost:22", 5*time.Second)
	if err != nil {
		return fmt.Errorf("dial local SSH: %w", err)
	}
	defer local.Close()
	log.Info("local SSH connected, sending ready")

	if err := wsSend(ws, map[string]string{"status": "ready"}); err != nil {
		return fmt.Errorf("send ready: %w", err)
	}

	// Bridge: relay WS ↔ localhost:22 using binary frames.
	done := make(chan struct{}, 1)
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := local.Read(buf)
			if n > 0 {
				ws.WriteMessage(websocket.BinaryMessage, buf[:n]) //nolint:errcheck
			}
			if err != nil {
				break
			}
		}
		done <- struct{}{}
	}()
	go func() {
		for {
			_, data, err := ws.ReadMessage()
			if len(data) > 0 {
				local.Write(data) //nolint:errcheck
			}
			if err != nil {
				break
			}
		}
		done <- struct{}{}
	}()
	<-done

	log.Info("session ended")
	return nil
}

func wsSend(ws *websocket.Conn, m map[string]string) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return ws.WriteMessage(websocket.BinaryMessage, append(data, '\n'))
}

func wsRecv(ws *websocket.Conn) (map[string]string, error) {
	return wsRecvDeadline(ws, 60*time.Second)
}

func wsRecvDeadline(ws *websocket.Conn, d time.Duration) (map[string]string, error) {
	ws.SetReadDeadline(time.Now().Add(d)) //nolint:errcheck
	defer ws.SetReadDeadline(time.Time{}) //nolint:errcheck
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
