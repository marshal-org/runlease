package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/runlease/runlease/pkg/credential"
)

func init() {
	rootCmd.AddCommand(connectCmd)
}

var connectCmd = &cobra.Command{
	Use:   "connect <name|id>",
	Short: "Open an SSH session through the runlease relay (no public IP required)",
	Args:  cobra.ExactArgs(1),
	RunE:  runConnect,
}

func runConnect(cmd *cobra.Command, args []string) error {
	reg, err := loadRegistry()
	if err != nil {
		return err
	}
	entry, err := reg.Get(args[0])
	if err != nil {
		return err
	}
	if entry.RelayAddr == "" {
		return fmt.Errorf("instance %s was not launched with a relay — use 'runlease ssh' instead", args[0])
	}

	masterKey, err := credential.LoadOrCreateMasterKey(defaultMasterKeyPath())
	if err != nil {
		return fmt.Errorf("loading master key: %w", err)
	}
	cred, err := credential.Load(entry.CredDir, string(entry.Handle.ID), masterKey)
	if err != nil {
		return fmt.Errorf("loading credential: %w", err)
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "🔌 Connecting to %s via relay (%s)...\n",
		entry.Handle.ManifestName, entry.RelayAddr)

	conn, err := dialRelay(entry.RelayAddr, entry.RelayToken)
	if err != nil {
		// Slot missing (relay restarted or first connect after a long gap).
		// Re-register the token and wait for the agent to reconnect.
		if !strings.Contains(err.Error(), "unauthorized") {
			return fmt.Errorf("relay: %w", err)
		}
		fmt.Fprintf(out, "⚠️  Relay slot missing (relay may have restarted) — re-registering...\n")
		if rerr := registerRelayToken(cmd.Context(), entry.RelayAddr, entry.RelayToken); rerr != nil {
			return fmt.Errorf("relay: %w", err) // return original error if re-register fails
		}
		return pollAndConnectViaRelay(cmd, entry.RelayAddr, entry.RelayToken,
			entry.Handle.ManifestName, cred)
	}
	return sshOverConn(conn, entry.RelayAddr, cred)
}
