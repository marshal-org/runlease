package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/marshal-org/runlease/pkg/credential"
)

func init() {
	rootCmd.AddCommand(connectCmd)
}

var connectCmd = &cobra.Command{
	Use:   "connect [name|id]",
	Short: "Open an SSH session through the runlease relay (no public IP required)",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runConnect,
}

func runConnect(cmd *cobra.Command, args []string) error {
	reg, err := loadRegistry()
	if err != nil {
		return err
	}
	nameOrID, err := resolveInstance(cmd, reg, args, "connect")
	if err != nil || nameOrID == "" {
		return err
	}
	entry, err := reg.Get(nameOrID)
	if err != nil {
		return err
	}
	if entry.RelayAddr == "" {
		return fmt.Errorf("instance %s was not launched with a relay — use 'runlease ssh' instead", entry.Handle.ManifestName)
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

	// Forwards declared at launch time are auto-applied so reconnecting
	// gives the same setup as the original `runlease up`.
	forwards := registryForwardsToSpecs(entry.Forwards)

	conn, err := dialRelay(entry.RelayAddr, entry.RelayToken)
	if err != nil {
		if strings.Contains(err.Error(), "unauthorized") {
			// Slot missing — relay restarted or first connect after a long gap.
			fmt.Fprintf(out, "⚠️  Relay slot missing (relay may have restarted) — re-registering...\n")
			if rerr := registerRelayToken(cmd.Context(), entry.RelayAddr, entry.RelayToken); rerr != nil {
				// Report why the slot could not be opened again (signed out, at
				// the tier limit), not the "unauthorized" that led here.
				return fmt.Errorf("relay: re-registering the slot: %w", rerr)
			}
			return pollAndConnectViaRelay(cmd, entry.RelayAddr, entry.RelayToken,
				entry.Handle.ManifestName, cred, forwards)
		}
		if strings.Contains(err.Error(), "close 1000") {
			// Relay dropped the connection cleanly — agent is mid-reconnect (5s delay).
			fmt.Fprintf(out, "⚠️  Agent reconnecting — waiting...\n")
			return pollAndConnectViaRelay(cmd, entry.RelayAddr, entry.RelayToken,
				entry.Handle.ManifestName, cred, forwards)
		}
		return fmt.Errorf("relay: %w", err)
	}
	return sshShellWithForwards(conn, entry.RelayAddr, cred, forwards, out)
}
