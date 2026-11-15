package main

import (
	"bufio"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/marshal-org/runlease/pkg/credential"
	"github.com/marshal-org/runlease/pkg/provider"
)

var downYes bool

func init() {
	downCmd.Flags().BoolVarP(&downYes, "yes", "y", false, "skip confirmation prompt")
	rootCmd.AddCommand(downCmd)
}

var downCmd = &cobra.Command{
	Use:   "down <name|id>",
	Short: "Terminate a spot instance and clean up credentials",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runDown,
}

func runDown(cmd *cobra.Command, args []string) error {
	reg, err := loadRegistry()
	if err != nil {
		return err
	}

	nameOrID, err := resolveInstance(cmd, reg, args, "down")
	if err != nil || nameOrID == "" {
		return err
	}

	entry, err := reg.Get(nameOrID)
	if err != nil {
		return err
	}

	if !downYes {
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "Terminate %s (%s, %s)? [y/N] ",
			entry.Handle.ManifestName, entry.Handle.ID, entry.Handle.Region)
		scanner := bufio.NewScanner(cmd.InOrStdin())
		scanner.Scan()
		if strings.ToLower(strings.TrimSpace(scanner.Text())) != "y" {
			fmt.Fprintln(out, "Aborted.")
			return nil
		}
	}

	ctx := cmd.Context()
	p, err := providerForEntry(ctx, entry.Handle, cmd.ErrOrStderr())
	if err != nil {
		return fmt.Errorf("initializing provider: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "🗑️  Terminating %s (%s)...\n", entry.Handle.ManifestName, entry.Handle.ID)
	done := "✅ Terminated."
	if err := p.Terminate(ctx, entry.Handle.ID); err != nil {
		// Not found is only proof the instance is gone when we looked where it
		// was created. Without a recorded project we may have looked in the
		// wrong one, so keep the entry rather than lose track of a live VM.
		if !errors.Is(err, provider.ErrNotFound) {
			return fmt.Errorf("terminating instance: %w\nRegistry entry kept — the instance may still be running", err)
		}
		if entry.Handle.ProjectUnknown() {
			return fmt.Errorf("terminating instance: %w\nRegistry entry kept — it may be running in another project. "+
				"Re-run with GOOGLE_CLOUD_PROJECT=<project it was launched in>", err)
		}
		done = fmt.Sprintf("⚠️  %v — already gone? Removed it from the registry.", err)
	}

	_ = credential.Delete(entry.CredDir, string(entry.Handle.ID))

	// Give the relay slot back now, so it stops counting against the owner's
	// tier. Best effort: the relay also drops a slot whose agent stays away.
	if entry.RelayAddr != "" && entry.RelayToken != "" {
		if err := releaseRelaySlot(ctx, entry.RelayAddr, entry.RelayToken); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "note: could not release the relay slot (%v). It will expire on its own.\n", err)
		}
	}

	if err := reg.RemoveID(entry.Handle.ID); err != nil {
		return fmt.Errorf("updating registry: %w", err)
	}

	fmt.Fprintln(cmd.OutOrStdout(), done)
	return nil
}
