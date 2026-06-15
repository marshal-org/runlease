package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/runlease/runlease/pkg/pricing"
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
	// Resolve spot rates for all instances concurrently. Each lookup is cached
	// (24h) and bounded by its own timeout, so a cold cache or a slow/credential-
	// less provider can't make `list` hang for timeout×N — the table waits at
	// most one timeout regardless of how many instances are listed.
	rates := fetchRates(cmd.Context(), entries)
	if jsonOutput() {
		return writeListJSON(cmd, entries, rates)
	}
	if len(entries) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No active instances.")
		return nil
	}

	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 1, ' ', 0)
	fmt.Fprintln(w, "NAME\tPROV\tID\tSTATE\tREGION\tAGE\tEXPIRES\t$/HR\tLEASE\tACCESS")
	var priceHint string
	for i, e := range entries {
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
		rate, rateErr := rates[i].rate, rates[i].err
		haveRate := rateErr == nil
		if rateErr != nil && priceHint == "" {
			priceHint = pricing.RemediationHint(rateErr)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			h.ManifestName, h.Provider, truncateID(string(h.ID)), h.State,
			h.Region, age, expires, fmtHourlyCell(rate, haveRate),
			fmtLeaseCell(rate, haveRate, e), access)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if priceHint != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "\n💡 $/HR unavailable — for live rates, %s\n", priceHint)
	}
	fmt.Fprintln(cmd.OutOrStdout(), "\nState is cached — run 'runlease sync' to refresh")
	return nil
}

// rateResult pairs a resolved rate with the error (if any) from its lookup.
type rateResult struct {
	rate pricing.Rate
	err  error
}

// fetchRates resolves spot rates for all entries concurrently, returning
// results aligned by index. Each goroutine writes its own slot, so there's no
// shared-state race; the pricing cache it touches is itself mutex-guarded.
func fetchRates(ctx context.Context, entries []*registry.Entry) []rateResult {
	results := make([]rateResult, len(entries))
	var wg sync.WaitGroup
	for i, e := range entries {
		wg.Add(1)
		go func(i int, e *registry.Entry) {
			defer wg.Done()
			r, err := instanceRate(ctx, e)
			results[i] = rateResult{rate: r, err: err}
		}(i, e)
	}
	wg.Wait()
	return results
}

// instanceRate fetches the spot rate for an entry, bounded by a timeout so a
// slow pricing API can't freeze `list`. Returns a non-nil error on any failure
// (or when the entry predates instance-type tracking) — the caller renders "-"
// and may surface a remediation hint from the error.
func instanceRate(ctx context.Context, e *registry.Entry) (pricing.Rate, error) {
	h := e.Handle
	if h.InstanceType == "" || h.Region == "" {
		return pricing.Rate{}, fmt.Errorf("no instance type recorded")
	}
	c, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	return pricing.SpotRate(c, runleaseHome(), h.Provider, h.InstanceType, h.Region)
}

// leaseDuration is the full TTL the instance was launched with (expiry minus
// creation), used to project total lease cost. Zero when there's no TTL.
func leaseDuration(e *registry.Entry) time.Duration {
	h := e.Handle
	if h.ExpiresAt == nil || h.CreatedAt.IsZero() {
		return 0
	}
	return h.ExpiresAt.Sub(h.CreatedAt)
}

func fmtHourlyCell(r pricing.Rate, ok bool) string {
	if !ok {
		return "-"
	}
	return fmtPriceCell(r.USDPerHour, r.Approx)
}

func fmtLeaseCell(r pricing.Rate, ok bool, e *registry.Entry) string {
	if !ok {
		return "-"
	}
	d := leaseDuration(e)
	if d <= 0 {
		return "-"
	}
	return fmtPriceCell(r.LeaseCost(d), r.Approx)
}

func fmtPriceCell(v float64, approx bool) string {
	if approx {
		return fmt.Sprintf("~%.3f", v)
	}
	return fmt.Sprintf("%.3f", v)
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
	Name         string            `json:"name"`
	ID           string            `json:"id"`
	Provider     string            `json:"provider"`
	InstanceType string            `json:"instanceType,omitempty"`
	State        string            `json:"state"`
	Region       string            `json:"region"`
	PublicIP     string            `json:"publicIp,omitempty"`
	PrivateIP    string            `json:"privateIp,omitempty"`
	Relay        string            `json:"relay,omitempty"`
	CreatedAt    string            `json:"createdAt,omitempty"`
	ExpiresAt    string            `json:"expiresAt,omitempty"`
	USDPerHour   *float64          `json:"usdPerHour,omitempty"`
	LeaseCostUSD *float64          `json:"leaseCostUsd,omitempty"`
	PriceApprox  bool              `json:"priceApprox,omitempty"`
	Tags         map[string]string `json:"tags,omitempty"`
}

func writeListJSON(cmd *cobra.Command, entries []*registry.Entry, rates []rateResult) error {
	out := make([]listJSONEntry, 0, len(entries))
	for i, e := range entries {
		h := e.Handle
		row := listJSONEntry{
			Name:         h.ManifestName,
			ID:           string(h.ID),
			Provider:     string(h.Provider),
			InstanceType: h.InstanceType,
			State:        string(h.State),
			Region:       h.Region,
			PublicIP:     h.PublicIP,
			PrivateIP:    h.PrivateIP,
			Relay:        e.RelayAddr,
			Tags:         h.Tags,
		}
		if !h.CreatedAt.IsZero() {
			row.CreatedAt = h.CreatedAt.UTC().Format(time.RFC3339)
		}
		if h.ExpiresAt != nil {
			row.ExpiresAt = h.ExpiresAt.UTC().Format(time.RFC3339)
		}
		if rates[i].err == nil {
			rate := rates[i].rate
			hourly := rate.USDPerHour
			row.USDPerHour = &hourly
			row.PriceApprox = rate.Approx
			if d := leaseDuration(e); d > 0 {
				lease := rate.LeaseCost(d)
				row.LeaseCostUSD = &lease
			}
		}
		out = append(out, row)
	}
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
