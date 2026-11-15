package main

import (
	"bufio"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/marshal-org/runlease/pkg/credential"
	"github.com/marshal-org/runlease/pkg/provider"
	"github.com/marshal-org/runlease/pkg/registry"
)

var (
	downYes    bool
	downLeaked bool
)

func init() {
	downCmd.Flags().BoolVarP(&downYes, "yes", "y", false, "skip confirmation prompt")
	downCmd.Flags().BoolVar(&downLeaked, "leaked", false, "delete runlease machines found in the cloud that this computer does not track and that should no longer exist: stopped ones (their disks still bill) and ones past their lease")
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
	if downLeaked {
		if len(args) > 0 {
			return fmt.Errorf("--leaked takes no instance name: it finds the machines itself")
		}
		return runDownLeaked(cmd, reg)
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

// runDownLeaked deletes the runlease machines that `list` reports as leaked or
// expired: found in the cloud by label, not in the registry, and either
// stopped with a disk still billing or running past their lease. A machine
// that is running within its lease is never touched: it is someone's.
func runDownLeaked(cmd *cobra.Command, reg *registry.Registry) error {
	out := cmd.OutOrStdout()
	ctx := cmd.Context()
	scopes := scanScopes(reg)
	if len(scopes) == 0 {
		return fmt.Errorf("nowhere to look: this computer tracks no machines and has no default provider and region. Run 'runlease configure' first")
	}
	found, notes := findUntracked(ctx, reg, scopes)
	var doomed []untrackedInstance
	for _, u := range found {
		if u.removable() {
			doomed = append(doomed, u)
		}
	}
	for _, n := range notes {
		fmt.Fprintf(cmd.ErrOrStderr(), "note: %s\n", n)
	}
	if len(doomed) == 0 {
		fmt.Fprintln(out, "Nothing leaked: no stopped or expired runlease machines found outside the registry.")
		return nil
	}

	fmt.Fprintf(out, "To be deleted (%d):\n", len(doomed))
	for _, u := range doomed {
		fmt.Fprintf(out, "  %s  %s %s  %s\n", u.ID, u.Provider, u.Region, explain(u))
	}
	if !downYes {
		fmt.Fprint(out, "Delete them? [y/N] ")
		scanner := bufio.NewScanner(cmd.InOrStdin())
		scanner.Scan()
		if strings.ToLower(strings.TrimSpace(scanner.Text())) != "y" {
			fmt.Fprintln(out, "Aborted.")
			return nil
		}
	}

	var failed int
	for _, u := range doomed {
		p, err := newProvider(ctx, u.Provider, u.Region, u.Project)
		if err == nil {
			err = p.Terminate(ctx, u.ID)
		}
		if err != nil && !errors.Is(err, provider.ErrNotFound) {
			failed++
			fmt.Fprintf(out, "❌ %s: %v\n", u.ID, err)
			continue
		}
		fmt.Fprintf(out, "✅ %s deleted\n", u.ID)
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d machines could not be deleted", failed, len(doomed))
	}
	return nil
}
