package main

import (
	"flag"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/runlease/runlease/pkg/relay"
)

func main() {
	addr := flag.String("addr", ":7777", "HTTP listen address")
	agentBinary := flag.String("agent-binary", "", "path to runlease-agent binary served at GET /agent (optional)")

	// Auth flags — leave audience empty to disable auth (local/private relay).
	audience := flag.String("audience", "", "Google ID token audience (e.g. https://relay.example.com). Empty disables auth.")
	allowedEmails := flag.String("allowed-emails", "", "comma-separated list of allowed Google email addresses")
	allowedDomains := flag.String("allowed-domains", "", "comma-separated list of allowed email domains (e.g. company.com)")

	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	cfg := relay.Config{
		AgentPath: *agentBinary,
		Auth: relay.AuthConfig{
			Audience:       *audience,
			AllowedEmails:  splitTrimmed(*allowedEmails),
			AllowedDomains: splitTrimmed(*allowedDomains),
		},
	}

	if cfg.Auth.Audience != "" {
		log.Info("auth enabled",
			"audience", cfg.Auth.Audience,
			"allowed_emails", cfg.Auth.AllowedEmails,
			"allowed_domains", cfg.Auth.AllowedDomains,
		)
	} else {
		log.Warn("auth disabled — relay accepts registrations from anyone")
	}

	srv := relay.New(log, cfg)

	log.Info("relay listening", "addr", *addr)
	if err := http.ListenAndServe(*addr, srv); err != nil {
		log.Error("relay server error", "err", err)
		os.Exit(1)
	}
}

func splitTrimmed(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
