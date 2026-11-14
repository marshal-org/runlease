// runlease-agent runs on each launched instance. It connects outbound to the
// relay over WebSocket, registers its token, then runs a yamux server on the
// WebSocket — accepting one stream per incoming CLI client and proxying each to
// localhost:22 (sshd). On disconnect it reconnects automatically.
//
// Required env vars (written to /etc/runlease-agent.env by user-data):
//   RUNLEASE_RELAY_ADDR  – relay address: host:port or ws[s]://host[:port]
//   RUNLEASE_RELAY_TOKEN – per-instance secret pre-registered by the CLI
//
// Optional:
//   RUNLEASE_LOCAL_TARGET – TCP target each yamux stream is bridged to.
//                           Defaults to "localhost:22".
package main

import (
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/marshal-org/runlease/pkg/agent"
)

func main() {
	relayAddr := os.Getenv("RUNLEASE_RELAY_ADDR")
	token := os.Getenv("RUNLEASE_RELAY_TOKEN")

	if relayAddr == "" || token == "" {
		fmt.Fprintln(os.Stderr, "RUNLEASE_RELAY_ADDR and RUNLEASE_RELAY_TOKEN must be set")
		os.Exit(1)
	}

	cfg := agent.Config{
		RelayAddr:   relayAddr,
		Token:       token,
		LocalTarget: os.Getenv("RUNLEASE_LOCAL_TARGET"),
		Logger:      slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}
	cfg.Logger.Info("runlease-agent starting", "relay", relayAddr)

	for {
		if err := agent.RunOnce(cfg); err != nil {
			cfg.Logger.Warn("session ended, reconnecting in 5s", "err", err)
		}
		time.Sleep(5 * time.Second)
	}
}
