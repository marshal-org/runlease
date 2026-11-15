package main

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	rlconfig "github.com/marshal-org/runlease/pkg/config"
	"github.com/marshal-org/runlease/pkg/provider"
	"github.com/marshal-org/runlease/pkg/registry"
)

// The local registry only knows the machines this computer launched and still
// remembers. A machine launched elsewhere, or one whose entry is gone, is
// invisible to it, and so is the disk such a machine keeps billing for when it
// powers off instead of deleting itself. The functions here ask the cloud
// instead: every instance carrying the runlease-managed label, compared with
// the registry.

// untrackedScanTimeout bounds the cloud lookups `list` makes, so a slow or
// unreachable provider delays the table by this much at most.
const untrackedScanTimeout = 12 * time.Second

// Why an untracked instance is reported.
const (
	// Powered off but not deleted: no compute cost, but its disk still bills.
	problemLeaked = "leaked"
	// Past its lease and still running: it should have deleted itself.
	problemExpired = "expired"
	// Running within its lease, just not launched from (or remembered by)
	// this computer. Not a problem by itself.
	problemElsewhere = "elsewhere"
)

type untrackedInstance struct {
	provider.ManagedInstance
	Problem string
}

// cloudScope is one place to look: a GCP project (every zone), or an AWS region.
type cloudScope struct {
	provider provider.ProviderType
	region   string // AWS region, or any GCP zone (the listing covers them all)
	project  string // GCP only; "" means the environment's project
}

func (s cloudScope) String() string {
	if s.provider == provider.ProviderGCP {
		if s.project == "" {
			return "gcp (default project)"
		}
		return "gcp project " + s.project
	}
	return fmt.Sprintf("%s %s", s.provider, s.region)
}

// scanScopes works out where to look: wherever this computer has launched
// before (the registry), and its configured defaults. With neither there is
// nothing to go on, and nothing is scanned.
func scanScopes(reg *registry.Registry) []cloudScope {
	seen := map[string]bool{}
	var scopes []cloudScope
	add := func(s cloudScope) {
		key := string(s.provider) + "|" + s.region
		if s.provider == provider.ProviderGCP {
			key = "gcp|" + s.project // one listing covers every zone
		}
		if s.provider == "" || s.region == "" || seen[key] {
			return
		}
		seen[key] = true
		scopes = append(scopes, s)
	}
	for _, e := range reg.List() {
		add(cloudScope{provider: e.Handle.Provider, region: e.Handle.Region, project: e.Handle.Project})
	}
	if cfg, err := rlconfig.Load(defaultConfigPath()); err == nil && cfg != nil {
		add(cloudScope{provider: provider.ProviderType(cfg.Defaults.Provider), region: cfg.Defaults.Region})
	}
	return scopes
}

// findUntracked lists runlease instances in each scope and returns those the
// registry does not hold, with why each is worth mentioning. Scopes that
// cannot be checked are returned as notes, not as an error: `list` must still
// show what it knows.
func findUntracked(ctx context.Context, reg *registry.Registry, scopes []cloudScope) (found []untrackedInstance, notes []string) {
	ctx, cancel := context.WithTimeout(ctx, untrackedScanTimeout)
	defer cancel()

	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, s := range scopes {
		wg.Add(1)
		go func(s cloudScope) {
			defer wg.Done()
			instances, err := listManaged(ctx, s)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				notes = append(notes, fmt.Sprintf("could not check %s: %v", s, err))
				return
			}
			for _, m := range instances {
				if _, err := reg.Get(string(m.ID)); err == nil {
					continue // tracked here
				}
				found = append(found, untrackedInstance{ManagedInstance: m, Problem: classify(m, time.Now())})
			}
		}(s)
	}
	wg.Wait()
	sort.Slice(found, func(i, j int) bool {
		if found[i].Problem != found[j].Problem {
			return problemRank(found[i].Problem) < problemRank(found[j].Problem)
		}
		return found[i].ID < found[j].ID
	})
	sort.Strings(notes)
	return found, notes
}

func listManaged(ctx context.Context, s cloudScope) ([]provider.ManagedInstance, error) {
	p, err := newProvider(ctx, s.provider, s.region, s.project)
	if err != nil {
		return nil, err
	}
	lister, ok := p.(provider.ManagedLister)
	if !ok {
		return nil, nil // this provider cannot list; nothing to report
	}
	return lister.ListManaged(ctx)
}

func classify(m provider.ManagedInstance, now time.Time) string {
	switch {
	case m.State == provider.StatusStopped:
		return problemLeaked
	case m.ExpiresAt != nil && now.After(*m.ExpiresAt):
		return problemExpired
	default:
		return problemElsewhere
	}
}

func problemRank(p string) int {
	switch p {
	case problemLeaked:
		return 0
	case problemExpired:
		return 1
	default:
		return 2
	}
}

// removable reports whether `down --leaked` should delete the instance: it is
// stopped with a disk still billing, or it has outlived its lease. A machine
// that is running within its lease belongs to someone and is left alone.
func (u untrackedInstance) removable() bool {
	return u.Problem == problemLeaked || u.Problem == problemExpired
}

// printUntracked writes the section `list` adds below its table.
func printUntracked(out io.Writer, found []untrackedInstance, notes []string) {
	if len(found) > 0 {
		fmt.Fprintf(out, "\n⚠  In your cloud account, but not tracked on this computer:\n\n")
		w := tabwriter.NewWriter(out, 0, 0, 1, ' ', 0)
		fmt.Fprintln(w, "NAME\tPROV\tID\tSTATE\tREGION\tAGE\tBY\tDISK\tWHY")
		for _, u := range found {
			disk := "-"
			if u.DiskGB > 0 {
				disk = fmt.Sprintf("%d GB", u.DiskGB)
				if u.DiskMonthlyUSD > 0 {
					disk += fmt.Sprintf(" (~$%.2f/mo)", u.DiskMonthlyUSD)
				}
			}
			age := "-"
			if !u.CreatedAt.IsZero() {
				age = fmtAge(u.CreatedAt)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				orDash(u.Name), u.Provider, u.ID, u.State, u.Region, age, orDash(u.CreatedBy), disk, explain(u))
		}
		w.Flush() //nolint:errcheck

		var removable int
		var monthly float64
		for _, u := range found {
			if u.removable() {
				removable++
				monthly += u.DiskMonthlyUSD
			}
		}
		if removable > 0 {
			cost := ""
			if monthly > 0 {
				cost = fmt.Sprintf(", costing about $%.2f a month in disks", monthly)
			}
			fmt.Fprintf(out, "\n   %d should not exist any more%s. Remove them with: runlease down --leaked\n", removable, cost)
		}
	}
	for _, n := range notes {
		fmt.Fprintf(out, "\nnote: %s\n", n)
	}
}

func explain(u untrackedInstance) string {
	switch u.Problem {
	case problemLeaked:
		return "stopped, not deleted: its disk still bills"
	case problemExpired:
		return "past its lease " + fmtDuration(time.Since(*u.ExpiresAt).Round(time.Minute)) + " ago, still running"
	default:
		return "launched elsewhere, within its lease"
	}
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}
