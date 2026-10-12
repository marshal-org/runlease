package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/runlease/runlease/pkg/credential"
	"github.com/runlease/runlease/pkg/provider"
)

func init() {
	rootCmd.AddCommand(syncCmd)
}

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Refresh instance states from cloud providers",
	RunE:  runSync,
}

func runSync(cmd *cobra.Command, _ []string) error {
	reg, err := loadRegistry()
	if err != nil {
		return err
	}
	entries := reg.List()
	if len(entries) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No active instances.")
		return nil
	}

	ctx := cmd.Context()
	out := cmd.OutOrStdout()
	for _, e := range entries {
		name := e.Handle.ManifestName
		p, err := providerForEntry(ctx, e.Handle, cmd.ErrOrStderr())
		if err != nil {
			fmt.Fprintf(out, "%-22s  error initializing provider: %v\n", name, err)
			continue
		}
		newState, err := p.Status(ctx, e.Handle.ID)
		if err != nil {
			if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "InvalidInstanceID") {
				newState = provider.StatusTerminated
			} else {
				fmt.Fprintf(out, "%-22s  error querying status: %v\n", name, err)
				continue
			}
		}

		old := e.Handle.State
		isDead := newState == provider.StatusTerminated || newState == provider.StatusStopped
		if isDead {
			if err := credential.Delete(e.CredDir, string(e.Handle.ID)); err != nil {
				fmt.Fprintf(out, "%-22s  warning: could not delete credentials: %v\n", name, err)
			}
			if err := reg.Remove(name); err != nil {
				fmt.Fprintf(out, "%-22s  error removing from registry: %v\n", name, err)
				continue
			}
			fmt.Fprintf(out, "%-22s  %s → %s  (removed)\n", name, old, newState)
		} else if newState != old {
			if err := reg.UpdateState(name, newState); err != nil {
				fmt.Fprintf(out, "%-22s  error updating registry: %v\n", name, err)
				continue
			}
			fmt.Fprintf(out, "%-22s  %s → %s\n", name, old, newState)
		} else {
			fmt.Fprintf(out, "%-22s  %s\n", name, old)
		}
	}
	return nil
}
