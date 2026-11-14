package main

import (
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/marshal-org/runlease/pkg/instanceclass"
	"github.com/marshal-org/runlease/pkg/provider"
)

func init() {
	rootCmd.AddCommand(catalogCmd)
}

var catalogCmd = &cobra.Command{
	Use:   "catalog",
	Short: "List available instance classes and their concrete types per provider",
	RunE:  runCatalog,
}

var catalogProviders = []provider.ProviderType{
	provider.ProviderAWS,
	provider.ProviderGCP,
	provider.ProviderAzure,
}

func runCatalog(cmd *cobra.Command, _ []string) error {
	resolver, err := instanceclass.LoadDefault()
	if err != nil {
		return fmt.Errorf("loading catalog: %w", err)
	}

	classes := []string{
		"compute.micro",
		"compute.small",
		"compute.medium",
		"compute.large",
		"compute.xlarge",
		"compute.2xlarge",
		"compute.gpu",
	}

	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "CLASS\tPROVIDER\tINSTANCE TYPES (preferred first)")

	for _, class := range classes {
		printed := false
		for _, pt := range catalogProviders {
			candidates, err := resolver.Resolve(class, pt)
			if err != nil {
				continue
			}
			className := class
			if printed {
				className = ""
			}
			fmt.Fprintf(w, "%s\t%s\t%s\n", className, pt, strings.Join(candidates, ", "))
			printed = true
		}
		if !printed {
			// class defined but no providers — skip silently
			continue
		}
	}

	_ = sort.Search // keep import tidy
	return w.Flush()
}
