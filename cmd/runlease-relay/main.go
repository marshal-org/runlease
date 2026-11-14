package main

import (
	"flag"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/marshal-org/runlease/pkg/relay"
)

func main() {
	addr := flag.String("addr", ":7777", "HTTP listen address")
	agentBinary := flag.String("agent-binary", "", "path to runlease-agent binary served at GET /agent (optional)")

	// Auth flags — leave audience empty to disable auth (local/private relay).
	audience := flag.String("audience", "", "Google ID token audience (e.g. https://relay.example.com). Empty disables auth.")
	allowedEmails := flag.String("allowed-emails", "", "comma-separated list of allowed Google email addresses")
	allowedDomains := flag.String("allowed-domains", "", "comma-separated list of allowed email domains (e.g. company.com)")

	insecureNoAuth := flag.Bool("insecure-no-auth", false, "run with authentication disabled: anyone who can reach the relay can register slots. For local development or a network you fully control.")
	maxSlots := flag.Int("max-slots", 5000, "maximum number of live slots; registration is refused beyond this. 0 disables the cap.")

	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	cfg := relay.Config{
		AgentPath: *agentBinary,
		MaxSlots:  *maxSlots,
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
	} else if *insecureNoAuth {
		log.Warn("auth disabled by --insecure-no-auth: relay accepts registrations from anyone who can reach it")
	} else {
		// Fail closed. A relay that silently starts open is the easiest way to
		// run a public tunnel endpoint by accident.
		log.Error("refusing to start without authentication: set --audience (and --allowed-emails or --allowed-domains), or pass --insecure-no-auth to run open on purpose")
		os.Exit(2)
	}

	srv := relay.New(log, cfg)

	log.Info("relay listening", "addr", *addr)
	// Bound the time a peer may take to send request headers, so idle half-open
	// connections cannot pile up. No read or write timeout: relay sessions are
	// long-lived WebSockets.
	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	if err := httpSrv.ListenAndServe(); err != nil {
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
