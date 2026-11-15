package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	rlconfig "github.com/marshal-org/runlease/pkg/config"
	"github.com/marshal-org/runlease/pkg/credential"
	"github.com/marshal-org/runlease/pkg/provider"
	"github.com/marshal-org/runlease/pkg/registry"
)

var (
	downYes      bool
	downLeaked   bool
	downProvider string
	downRegion   string
	downProject  string
)

func init() {
	downCmd.Flags().BoolVarP(&downYes, "yes", "y", false, "skip confirmation prompt")
	downCmd.Flags().BoolVar(&downLeaked, "leaked", false, "delete runlease machines found in the cloud that this computer does not track, are stopped, and are past their lease (their disks still bill). Never deletes a running machine")
	downCmd.Flags().StringVarP(&downProvider, "provider", "p", "", "with --leaked: the cloud to look in, gcp or aws (default: where this computer has launched before, its saved default, or every cloud signed in here)")
	downCmd.Flags().StringVarP(&downRegion, "region", "r", "", "with --leaked: the AWS region to look in. On Google Cloud a zone only says which cloud: every zone of the project is searched")
	downCmd.Flags().StringVar(&downProject, "project", "", "with --leaked: the Google Cloud project to look in, all zones (default: the one your Google Cloud setup names)")
	rootCmd.AddCommand(downCmd)
}

var downCmd = &cobra.Command{
	Use:   "down <name|id>",
	Short: "Terminate a spot instance and clean up credentials",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runDown,
}

// downJSON is what `down --output json` prints for the machine it terminated.
type downJSON struct {
	Name     string `json:"name"`
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Region   string `json:"region"`
	// Terminated is true once the machine is gone and forgotten. AlreadyGone
	// adds that the cloud no longer had it when asked.
	Terminated  bool `json:"terminated"`
	AlreadyGone bool `json:"alreadyGone"`
}

func runDown(cmd *cobra.Command, args []string) error {
	// The prompt is written to the same stream as the result, so JSON output
	// cannot ask.
	if jsonOutput() && !downYes {
		return fmt.Errorf("--output json cannot ask for confirmation: add --yes")
	}
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
	if downProvider != "" || downRegion != "" || downProject != "" {
		return fmt.Errorf("--provider, --region and --project go with --leaked: a tracked machine is found by its name")
	}
	if jsonOutput() && len(args) == 0 && len(reg.List()) != 1 {
		return fmt.Errorf("--output json needs the name of the instance to terminate: this computer tracks %d", len(reg.List()))
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

	if !jsonOutput() {
		fmt.Fprintf(cmd.OutOrStdout(), "🗑️  Terminating %s (%s)...\n", entry.Handle.ManifestName, entry.Handle.ID)
	}
	done := "✅ Terminated."
	alreadyGone := false
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
		alreadyGone = true
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

	if jsonOutput() {
		return writeJSON(cmd.OutOrStdout(), downJSON{
			Name: entry.Handle.ManifestName, ID: string(entry.Handle.ID),
			Provider: string(entry.Handle.Provider), Region: entry.Handle.Region,
			Terminated: true, AlreadyGone: alreadyGone,
		})
	}
	fmt.Fprintln(cmd.OutOrStdout(), done)
	return nil
}

// leakedJSON is what `down --leaked --output json` prints: where it looked and
// what became of each machine it meant to delete.
type leakedJSON struct {
	Scopes  []string        `json:"scopes"`
	Deleted []leakedMachine `json:"deleted"`
	Kept    []leakedMachine `json:"kept"`   // not deleted after all; Reason says why
	Failed  []leakedMachine `json:"failed"` // Error says why
	// Notes are places that could not be checked. With any, an empty Deleted
	// is not an all-clear.
	Notes []string `json:"notes"`
}

type leakedMachine struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Provider string `json:"provider"`
	Region   string `json:"region"`
	Project  string `json:"project,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Error    string `json:"error,omitempty"`
}

func leakedRow(u untrackedInstance) leakedMachine {
	return leakedMachine{ID: string(u.ID), Name: u.Name, Provider: string(u.Provider), Region: u.Region, Project: u.Project}
}

// runDownLeaked deletes the runlease machines that `list` reports as leaked:
// found in the cloud by label, not in the registry, stopped with a disk still
// billing, and past their lease. A running machine is never touched: it may
// be someone's.
func runDownLeaked(cmd *cobra.Command, reg *registry.Registry) error {
	out := cmd.OutOrStdout()
	ctx := cmd.Context()
	// A config file that cannot be read is not an empty one: the saved default
	// may be the place the user means.
	cfg, err := rlconfig.Load(defaultConfigPath())
	if err != nil {
		return fmt.Errorf("%w\nRepair or remove %s, or say where to look with --provider, --region and --project after that", err, defaultConfigPath())
	}
	plan, err := resolveScopes(reg, cfg, scopeFlags{downProvider, downRegion, downProject}, true)
	if err != nil {
		return err
	}
	if len(plan.scopes) == 0 {
		return errNowhereToLook
	}
	// Before anything is listed or deleted: where, by its real name.
	plan = locateScopes(ctx, plan)
	scopes := plan.scopes
	announceScopes(cmd.ErrOrStderr(), plan)

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
	result := leakedJSON{Scopes: scopeNames(scopes), Deleted: []leakedMachine{}, Kept: []leakedMachine{}, Failed: []leakedMachine{}, Notes: notes}
	if result.Notes == nil {
		result.Notes = []string{}
	}
	if len(doomed) == 0 {
		if jsonOutput() {
			if err := writeJSON(out, result); err != nil {
				return err
			}
		}
		if len(notes) > 0 {
			// A place that could not be asked is not a place with nothing in it.
			return fmt.Errorf("%d of %d places could not be checked, so this is not an all-clear; nothing leaked was found in the rest", len(notes), len(scopes))
		}
		if !jsonOutput() {
			fmt.Fprintln(out, "Nothing leaked: no stopped or expired runlease machines found outside the registry.")
		}
		return nil
	}

	// Name and creator are shown because these are machines this computer did
	// not launch: whoever answers the prompt should see whose they are.
	if !jsonOutput() {
		fmt.Fprintf(out, "To be deleted (%d):\n", len(doomed))
		for _, u := range doomed {
			fmt.Fprintf(out, "  %s  name %s, by %s  %s %s  %s\n", u.ID, orDash(u.Name), orDash(u.CreatedBy), u.Provider, u.Region, explain(u))
		}
	}
	// Deleting needs a place somebody named or a person who has seen it. With
	// --yes (and so with JSON output) nobody is asked, so a place that was
	// filled in is listed and left alone.
	if plan.unnamed != "" && (downYes || jsonOutput()) {
		const reason = "not deleted: the place was not named, and nobody was asked"
		for _, u := range doomed {
			row := leakedRow(u)
			row.Reason = reason
			result.Kept = append(result.Kept, row)
		}
		if jsonOutput() {
			if err := writeJSON(out, result); err != nil {
				return err
			}
		}
		var cmds []string
		for _, s := range scopes {
			cmds = append(cmds, "  runlease down --leaked --yes "+s.flags())
		}
		return fmt.Errorf("nothing was deleted: %s, and --yes does not confirm a place nobody named.\n"+
			"Name the place to delete in (or run without --yes to be asked):\n%s", plan.unnamed, strings.Join(cmds, "\n"))
	}
	if !downYes && !confirm(cmd, "Delete them?") {
		fmt.Fprintln(out, "Aborted.")
		return nil
	}

	for _, u := range doomed {
		kept, err := deleteLeaked(ctx, u)
		row := leakedRow(u)
		switch {
		case err != nil:
			row.Error = err.Error()
			result.Failed = append(result.Failed, row)
			if !jsonOutput() {
				fmt.Fprintf(out, "❌ %s: %v\n", u.ID, err)
			}
		case kept != "":
			row.Reason = kept
			result.Kept = append(result.Kept, row)
			if !jsonOutput() {
				fmt.Fprintf(out, "⏭️  %s left alone: %s\n", u.ID, kept)
			}
		default:
			result.Deleted = append(result.Deleted, row)
			if !jsonOutput() {
				fmt.Fprintf(out, "✅ %s deleted\n", u.ID)
			}
		}
	}
	if jsonOutput() {
		if err := writeJSON(out, result); err != nil {
			return err
		}
	}
	if failed := len(result.Failed); failed > 0 {
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
