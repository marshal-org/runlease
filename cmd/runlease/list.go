package main

import (
	"encoding/json"
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
	if jsonOutput() {
		return writeListJSON(cmd, entries)
	}
	if len(entries) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No active instances.")
		return nil
	}

	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 1, ' ', 0)
	fmt.Fprintln(w, "NAME\tPROV\tID\tSTATE\tREGION\tAGE\tEXPIRES\tACCESS")
	for _, e := range entries {
		h := e.Handle
		age := fmtAge(h.CreatedAt)
		expires := "-"
		if h.ExpiresAt != nil {
			remaining := time.Until(*h.ExpiresAt).Round(time.Second)
			if remaining < 0 {
				expires = "expired"
			} else {
				expires = fmtDuration(remaining)
			}
		}
		access := h.PublicIP
		if e.RelayAddr != "" {
			access = "relay"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			h.ManifestName, h.Provider, truncateID(string(h.ID)), h.State,
			h.Region, age, expires, access)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), "\nState is cached — run 'runlease sync' to refresh")
	return nil
}

// truncateID shortens long cloud IDs (e.g. AWS "i-0988371fe8a766d47" → "i-0988371fe8…")
// GCP instance names are already short enough to show in full.
func truncateID(id string) string {
	const max = 12
	if len(id) <= max {
		return id
	}
	return id[:max] + "~"
}

func fmtAge(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return fmtDuration(time.Since(t))
}

func fmtDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		m := int(d.Minutes())
		s := int(d.Seconds()) % 60
		if s == 0 {
			return fmt.Sprintf("%dm", m)
		}
		return fmt.Sprintf("%dm%ds", m, s)
	}
	if d < 24*time.Hour {
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh%dm", h, m)
	}
	days := int(d.Hours()) / 24
	h := int(d.Hours()) % 24
	if h == 0 {
		return fmt.Sprintf("%dd", days)
	}
	return fmt.Sprintf("%dd%dh", days, h)
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

// listJSONEntry is the public-shape JSON record for one instance. We expose a
// stable subset of the registry entry so callers can rely on the schema even
// if internal fields shift.
type listJSONEntry struct {
	Name      string            `json:"name"`
	ID        string            `json:"id"`
	Provider  string            `json:"provider"`
	State     string            `json:"state"`
	Region    string            `json:"region"`
	PublicIP  string            `json:"publicIp,omitempty"`
	PrivateIP string            `json:"privateIp,omitempty"`
	Relay     string            `json:"relay,omitempty"`
	CreatedAt string            `json:"createdAt,omitempty"`
	ExpiresAt string            `json:"expiresAt,omitempty"`
	Tags      map[string]string `json:"tags,omitempty"`
}

func writeListJSON(cmd *cobra.Command, entries []*registry.Entry) error {
	out := make([]listJSONEntry, 0, len(entries))
	for _, e := range entries {
		h := e.Handle
		row := listJSONEntry{
			Name:      h.ManifestName,
			ID:        string(h.ID),
			Provider:  string(h.Provider),
			State:     string(h.State),
			Region:    h.Region,
			PublicIP:  h.PublicIP,
			PrivateIP: h.PrivateIP,
			Relay:     e.RelayAddr,
			Tags:      h.Tags,
		}
		if !h.CreatedAt.IsZero() {
			row.CreatedAt = h.CreatedAt.UTC().Format(time.RFC3339)
		}
		if h.ExpiresAt != nil {
			row.ExpiresAt = h.ExpiresAt.UTC().Format(time.RFC3339)
		}
		out = append(out, row)
	}
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
