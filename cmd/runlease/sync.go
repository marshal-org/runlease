package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/marshal-org/runlease/pkg/credential"
	"github.com/marshal-org/runlease/pkg/provider"
)

func init() {
	rootCmd.AddCommand(syncCmd)
}

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Refresh instance states from cloud providers",
	RunE:  runSync,
}

// syncJSON is one tracked machine in `sync --output json`.
type syncJSON struct {
	Name          string `json:"name"`
	ID            string `json:"id"`
	Provider      string `json:"provider"`
	Region        string `json:"region"`
	PreviousState string `json:"previousState"`
	State         string `json:"state"` // what the cloud says now; "" if it could not be asked
	// Action is what sync did: "unchanged", "updated" (the recorded state),
	// "removed" (gone or stopped, so deleted and forgotten), "kept" (looks
	// gone but may only be in another project) or "error".
	Action string `json:"action"`
	Error  string `json:"error,omitempty"`
}

func runSync(cmd *cobra.Command, _ []string) error {
	reg, err := loadRegistry()
	if err != nil {
		return err
	}
	entries := reg.List()
	out := cmd.OutOrStdout()
	if len(entries) == 0 {
		if jsonOutput() {
			return writeJSON(out, []syncJSON{})
		}
		fmt.Fprintln(out, "No active instances.")
		return nil
	}

	ctx := cmd.Context()
	rows := make([]syncJSON, 0, len(entries))
	for _, e := range entries {
		name := e.Handle.ManifestName
		row := syncJSON{Name: name, ID: string(e.Handle.ID), Provider: string(e.Handle.Provider),
			Region: e.Handle.Region, PreviousState: string(e.Handle.State)}
		// say prints the line for a person; fail also records it for JSON.
		say := func(format string, a ...any) {
			if !jsonOutput() {
				fmt.Fprintf(out, "%-22s  "+format+"\n", append([]any{name}, a...)...)
			}
		}
		fail := func(format string, a ...any) {
			row.Action, row.Error = "error", fmt.Sprintf(format, a...)
			say(format, a...)
			rows = append(rows, row)
		}
		p, err := providerForEntry(ctx, e.Handle, cmd.ErrOrStderr())
		if err != nil {
			fail("error initializing provider: %v", err)
			continue
		}
		newState, err := p.Status(ctx, e.Handle.ID)
		if err != nil {
			if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "InvalidInstanceID") {
				newState = provider.StatusTerminated
			} else {
				fail("error querying status: %v", err)
				continue
			}
		}
		row.State = string(newState)

		old := e.Handle.State
		isDead := newState == provider.StatusTerminated || newState == provider.StatusStopped
		if newState == provider.StatusTerminated && e.Handle.ProjectUnknown() {
			// A 404 reads as terminated, but without a recorded project we may
			// have looked in the wrong one. Keep the entry.
			say("not found in the default project; kept (no recorded project) — "+
				"run `GOOGLE_CLOUD_PROJECT=<project> runlease down %s`", name)
			row.Action = "kept"
			row.Error = "not found in the default project, and no project is recorded for it"
			rows = append(rows, row)
			continue
		}
		if newState == provider.StatusStopped {
			// Powered off is not deleted: the machine and its disk still exist
			// and are still billed. Delete it before forgetting it.
			if err := p.Terminate(ctx, e.Handle.ID); err != nil && !errors.Is(err, provider.ErrNotFound) {
				fail("stopped, and deleting it failed: %v (kept)", err)
				continue
			}
		}
		if isDead {
			if err := credential.Delete(e.CredDir, string(e.Handle.ID)); err != nil {
				say("warning: could not delete credentials: %v", err)
			}
			// The token leaves with the registry entry, so this is the last
			// chance to give the relay slot back. Best effort, as in 'down'.
			if e.RelayAddr != "" && e.RelayToken != "" {
				_ = releaseRelaySlot(ctx, e.RelayAddr, e.RelayToken)
			}
			if err := reg.RemoveID(e.Handle.ID); err != nil {
				fail("error removing from registry: %v", err)
				continue
			}
			say("%s → %s  (removed)", old, newState)
			row.Action = "removed"
		} else if newState != old {
			if err := reg.UpdateState(name, newState); err != nil {
				fail("error updating registry: %v", err)
				continue
			}
			say("%s → %s", old, newState)
			row.Action = "updated"
		} else {
			say("%s", old)
			row.Action = "unchanged"
		}
		rows = append(rows, row)
	}
	if jsonOutput() {
		return writeJSON(out, rows)
	}
	return nil
}
