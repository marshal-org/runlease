package main

import (
	"bufio"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/runlease/runlease/pkg/credential"
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

	var nameOrID string
	if len(args) == 0 {
		entries := reg.List()
		switch len(entries) {
		case 0:
			fmt.Fprintln(cmd.OutOrStdout(), "No running instances.")
			return nil
		case 1:
			nameOrID = entries[0].Handle.ManifestName
		default:
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Multiple instances running — specify one:\n\n")
			for _, e := range entries {
				fmt.Fprintf(out, "  runlease down %s\n", e.Handle.ManifestName)
			}
			fmt.Fprintln(out)
			return nil
		}
	} else {
		nameOrID = args[0]
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
	p, err := newProvider(ctx, entry.Handle.Provider, entry.Handle.Region)
	if err != nil {
		return fmt.Errorf("initializing provider: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "🗑️  Terminating %s (%s)...\n", entry.Handle.ManifestName, entry.Handle.ID)
	if err := p.Terminate(ctx, entry.Handle.ID); err != nil {
		return fmt.Errorf("terminating instance: %w", err)
	}

	_ = credential.Delete(entry.CredDir, string(entry.Handle.ID))

	if err := reg.Remove(nameOrID); err != nil {
		return fmt.Errorf("updating registry: %w", err)
	}

	fmt.Fprintln(cmd.OutOrStdout(), "✅ Terminated.")
	return nil
}
