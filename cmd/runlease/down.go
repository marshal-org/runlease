package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/marshal-org/runlease/pkg/credential"
	"github.com/marshal-org/runlease/pkg/provider"
	"github.com/marshal-org/runlease/pkg/registry"
)

var (
	downYes      bool
	downLeaked   bool
	downProvider string
	downRegion   string
)

func init() {
	downCmd.Flags().BoolVarP(&downYes, "yes", "y", false, "skip confirmation prompt")
	downCmd.Flags().BoolVar(&downLeaked, "leaked", false, "delete runlease machines found in the cloud that this computer does not track, are stopped, and are past their lease (their disks still bill). Never deletes a running machine")
	downCmd.Flags().StringVarP(&downProvider, "provider", "p", "", "with --leaked: the cloud to look in, gcp or aws (default: where this computer has launched before, its saved default, or every cloud signed in here)")
	downCmd.Flags().StringVarP(&downRegion, "region", "r", "", "with --leaked: the AWS region to look in, or any zone of the Google Cloud project (default: that cloud's default)")
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
	if downProvider != "" || downRegion != "" {
		return fmt.Errorf("--provider and --region go with --leaked: a tracked machine is found by its name")
	}

	nameOrID, err := resolveInstance(cmd, reg, args, "down")
	if err != nil || nameOrID == "" {
		return err
	}

	entry, err := reg.Get(nameOrID)
	if err != nil {
		return err
	}

	if !downYes && !confirm(cmd, fmt.Sprintf("Terminate %s (%s, %s)?",
		entry.Handle.ManifestName, entry.Handle.ID, entry.Handle.Region)) {
		fmt.Fprintln(cmd.OutOrStdout(), "Aborted.")
		return nil
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

// runDownLeaked deletes the runlease machines that `list` reports as leaked:
// found in the cloud by label, not in the registry, stopped with a disk still
// billing, and past their lease. A running machine is never touched: it may
// be someone's.
func runDownLeaked(cmd *cobra.Command, reg *registry.Registry) error {
	out := cmd.OutOrStdout()
	ctx := cmd.Context()
	scopes, why, err := resolveScopes(reg, downProvider, downRegion, true)
	if err != nil {
		return err
	}
	if len(scopes) == 0 {
		return errNowhereToLook
	}
	if why != "" {
		// Nobody chose these places, so say which they are.
		fmt.Fprintf(cmd.ErrOrStderr(), "Looking in %s (%s). Choose with --provider and --region.\n", strings.Join(scopeNames(scopes), " and "), why)
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
		if len(notes) > 0 {
			// A place that could not be asked is not a place with nothing in it.
			return fmt.Errorf("%d of %d places could not be checked, so this is not an all-clear; nothing leaked was found in the rest", len(notes), len(scopes))
		}
		fmt.Fprintln(out, "Nothing leaked: no stopped or expired runlease machines found outside the registry.")
		return nil
	}

	// Name and creator are shown because these are machines this computer did
	// not launch: whoever answers the prompt should see whose they are.
	fmt.Fprintf(out, "To be deleted (%d):\n", len(doomed))
	for _, u := range doomed {
		fmt.Fprintf(out, "  %s  name %s, by %s  %s %s  %s\n", u.ID, orDash(u.Name), orDash(u.CreatedBy), u.Provider, u.Region, explain(u))
	}
	if !downYes && !confirm(cmd, "Delete them?") {
		fmt.Fprintln(out, "Aborted.")
		return nil
	}

	var failed int
	for _, u := range doomed {
		kept, err := deleteLeaked(ctx, u)
		switch {
		case err != nil:
			failed++
			fmt.Fprintf(out, "❌ %s: %v\n", u.ID, err)
		case kept != "":
			fmt.Fprintf(out, "⏭️  %s left alone: %s\n", u.ID, kept)
		default:
			fmt.Fprintf(out, "✅ %s deleted\n", u.ID)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d machines could not be deleted", failed, len(doomed))
	}
	return nil
}

// deleteLeaked deletes one machine the scan found. kept is set, with the
// reason, when the machine turned out not to need deleting after all.
func deleteLeaked(ctx context.Context, u untrackedInstance) (kept string, err error) {
	p, err := newProvider(ctx, u.Provider, u.Region, u.Project)
	if err != nil {
		return "", err
	}
	if u.Problem == problemLeaked {
		// The listing is as old as the prompt was left open. A machine is
		// deleted for being stopped only if it still is: its owner may have
		// started it again in the meantime.
		status, err := p.Status(ctx, u.ID)
		if err != nil {
			return "", fmt.Errorf("checking it is still stopped: %w", err)
		}
		switch status {
		case provider.StatusStopped:
		case provider.StatusTerminated:
			return "", nil // gone already
		default:
			return fmt.Sprintf("it is %s now, not stopped", status), nil
		}
	}
	if err := p.Terminate(ctx, u.ID); err != nil && !errors.Is(err, provider.ErrNotFound) {
		return "", err
	}
	return "", nil
}

// confirm asks a yes/no question and reports whether the answer was "y".
// Anything else, including no answer at all, is a no.
func confirm(cmd *cobra.Command, question string) bool {
	fmt.Fprintf(cmd.OutOrStdout(), "%s [y/N] ", question)
	scanner := bufio.NewScanner(cmd.InOrStdin())
	scanner.Scan()
	return strings.ToLower(strings.TrimSpace(scanner.Text())) == "y"
}
