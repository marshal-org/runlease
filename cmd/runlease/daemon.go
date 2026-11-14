package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/marshal-org/runlease/pkg/lifecycle"
)

func init() {
	rootCmd.AddCommand(daemonCmd)
}

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Run the reclaim engine (watches TTLs, cleans up terminated instances)",
	Long: `Starts a long-running process that reconciles the local registry against the
actual cloud state on startup, then watches active leases and terminates them
when their TTL expires. Run under systemd, screen, or tmux.

On startup the daemon will:
  1. Query the provider for every registry entry
  2. Remove entries for instances that are already terminated
  3. Immediately terminate instances whose TTL has already elapsed
  4. Arm a timer for each instance still within its TTL

On SIGTERM or Ctrl-C the daemon exits cleanly; any armed timers will be
re-evaluated on the next start via the same reconciliation process.`,
	RunE: runDaemon,
}

func runDaemon(cmd *cobra.Command, _ []string) error {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	reg, err := loadRegistry()
	if err != nil {
		return fmt.Errorf("loading registry: %w", err)
	}

	mgr := lifecycle.New(reg, newProvider, logger)
	mgr.Start(cmd.Context())
	return nil
}
