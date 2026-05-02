package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/runlease/runlease/pkg/credential"
)

func init() {
	rootCmd.AddCommand(downCmd)
}

var downCmd = &cobra.Command{
	Use:   "down <name|id>",
	Short: "Terminate a spot instance and clean up credentials",
	Args:  cobra.ExactArgs(1),
	RunE:  runDown,
}

func runDown(cmd *cobra.Command, args []string) error {
	nameOrID := args[0]

	reg, err := loadRegistry()
	if err != nil {
		return err
	}
	entry, err := reg.Get(nameOrID)
	if err != nil {
		return err
	}

	ctx := cmd.Context()
	p, err := newProvider(ctx, entry.Handle.Provider, entry.Handle.Region)
	if err != nil {
		return fmt.Errorf("initializing provider: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Terminating %s (%s)...\n", entry.Handle.ManifestName, entry.Handle.ID)
	if err := p.Terminate(ctx, entry.Handle.ID); err != nil {
		return fmt.Errorf("terminating instance: %w", err)
	}

	_ = credential.Delete(entry.CredDir, string(entry.Handle.ID))

	if err := reg.Remove(nameOrID); err != nil {
		return fmt.Errorf("updating registry: %w", err)
	}

	fmt.Fprintln(cmd.OutOrStdout(), "Terminated.")
	return nil
}
