package main

import (
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/runlease/runlease/pkg/registry"
)

var labelFilters []string

func init() {
	listCmd.Flags().StringArrayVarP(&labelFilters, "label", "l", nil, "filter by label key=value (repeatable, ANDed)")
	rootCmd.AddCommand(listCmd)
}

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List active instances",
	RunE:  runList,
}

func runList(cmd *cobra.Command, _ []string) error {
	reg, err := loadRegistry()
	if err != nil {
		return err
	}

	filters, err := parseLabels(labelFilters)
	if err != nil {
		return err
	}

	entries := filterEntries(reg.List(), filters)
	if len(entries) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No active instances.")
		return nil
	}

	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "NAME\tPROVIDER\tID\tSTATE\tPUBLIC IP\tREGION\tAGE\tEXPIRES")
	for _, e := range entries {
		h := e.Handle
		age := time.Since(h.CreatedAt).Round(time.Second)
		expires := "-"
		if h.ExpiresAt != nil {
			remaining := time.Until(*h.ExpiresAt).Round(time.Second)
			if remaining < 0 {
				expires = "expired"
			} else {
				expires = remaining.String()
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			h.ManifestName, h.Provider, h.ID, h.State,
			h.PublicIP, h.Region, age, expires)
	}
	return w.Flush()
}

func parseLabels(raw []string) (map[string]string, error) {
	out := make(map[string]string, len(raw))
	for _, s := range raw {
		k, v, ok := strings.Cut(s, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("invalid label filter %q: expected key=value", s)
		}
		out[k] = v
	}
	return out, nil
}

func filterEntries(entries []*registry.Entry, filters map[string]string) []*registry.Entry {
	if len(filters) == 0 {
		return entries
	}
	var out []*registry.Entry
	for _, e := range entries {
		if matchesLabels(e.Handle.Tags, filters) {
			out = append(out, e)
		}
	}
	return out
}

func matchesLabels(tags, filters map[string]string) bool {
	for k, v := range filters {
		if tags[k] != v {
			return false
		}
	}
	return true
}
