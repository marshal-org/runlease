package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/marshal-org/runlease/pkg/relay"
)

// version is set at release time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	addr := flag.String("addr", ":7777", "HTTP listen address")
	agentBinary := flag.String("agent-binary", "", "path to runlease-agent binary served at GET /agent (optional)")
	agentCacheDir := flag.String("agent-cache-dir", filepath.Join(os.TempDir(), "runlease-relay-agents"), "where agents of other releases are kept once fetched, so every CLI release finds its own agent at GET /agent/sha256/<hex>. Empty serves only --agent-binary by digest.")
	agentReleaseURL := flag.String("agent-release-url", relay.DefaultAgentReleaseURL, "URL of a release's linux/amd64 agent; {version} is replaced by the tag the CLI names. The relay keeps a download only when its SHA-256 is the one asked for. Empty turns fetching off.")

	// Auth flags — leave audience empty to disable auth (local/private relay).
	audience := flag.String("audience", "", "Google ID token audience (e.g. https://relay.example.com). Empty disables auth.")
	allowedEmails := flag.String("allowed-emails", "", "comma-separated list of allowed Google email addresses")
	allowedDomains := flag.String("allowed-domains", "", "comma-separated list of allowed email domains (e.g. company.com)")

	googleClientID := flag.String("google-client-id", "", "comma-separated Google OAuth client IDs whose ID tokens are accepted. 'runlease login' signs users in with the first one.")
	googleClientSecret := flag.String("google-client-secret", "", "client secret of the first --google-client-id, published to CLIs for the sign-in flow (not confidential for an installed-application client). Also read from RUNLEASE_GOOGLE_CLIENT_SECRET.")
	tiersFile := flag.String("tiers-file", "", "YAML file of tiers and per-identity limits. Without it there are no per-identity limits.")
	tiersReload := flag.Duration("tiers-reload", 15*time.Second, "how often to re-read --tiers-file and apply a change without a restart. 0 reads it only at start and on SIGHUP.")
	insecureNoAuth := flag.Bool("insecure-no-auth", false, "run with authentication disabled: anyone who can reach the relay can register slots. For local development or a network you fully control.")
	maxSlots := flag.Int("max-slots", 5000, "maximum number of live slots; registration is refused beyond this. 0 disables the cap.")

	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	if *googleClientSecret == "" {
		*googleClientSecret = os.Getenv("RUNLEASE_GOOGLE_CLIENT_SECRET")
	}
	var tiers *relay.Tiers
	if *tiersFile != "" {
		var err error
		if tiers, err = relay.LoadTiers(*tiersFile); err != nil {
			log.Error("cannot load tiers", "err", err)
			os.Exit(2)
		}
	}

	cfg := relay.Config{
		Tiers:              tiers,
		GoogleClientSecret: *googleClientSecret,
		Version:            version,
		AgentPath:          *agentBinary,
		AgentCacheDir:      *agentCacheDir,
		AgentReleaseURL:    *agentReleaseURL,
		MaxSlots:           *maxSlots,
		Auth: relay.AuthConfig{
			Audience:        *audience,
			GoogleClientIDs: splitTrimmed(*googleClientID),
			AllowedEmails:   splitTrimmed(*allowedEmails),
			AllowedDomains:  splitTrimmed(*allowedDomains),
		},
	}

	if cfg.Auth.Audience != "" || len(cfg.Auth.GoogleClientIDs) > 0 {
		if tiers != nil {
			log.Info("tiers loaded", "default", tiers.Default, "tiers", len(tiers.Tiers))
		}
		log.Info("auth enabled",
			"audience", cfg.Auth.Audience,
			"google_client_ids", cfg.Auth.GoogleClientIDs,
			"allowed_emails", cfg.Auth.AllowedEmails,
			"allowed_domains", cfg.Auth.AllowedDomains,
		)
	} else if *insecureNoAuth {
		log.Warn("auth disabled by --insecure-no-auth: relay accepts registrations from anyone who can reach it")
	} else {
		// Fail closed. A relay that silently starts open is the easiest way to
		// run a public tunnel endpoint by accident.
		log.Error("refusing to start without authentication: set --audience or --google-client-id, or pass --insecure-no-auth to run open on purpose")
		os.Exit(2)
	}

	srv := relay.New(log, cfg)

	// Limits can change without a restart: connected sessions are untouched,
	// and a file that is not valid leaves the limits in force as they are.
	if *tiersFile != "" {
		if *tiersReload > 0 {
			go srv.WatchTiers(context.Background(), *tiersFile, *tiersReload)
		}
		hup := make(chan os.Signal, 1)
		signal.Notify(hup, syscall.SIGHUP)
		go func() {
			for range hup {
				srv.ReloadTiersNow(*tiersFile)
			}
		}()
	}

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
