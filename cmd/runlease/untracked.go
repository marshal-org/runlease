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

// leaseGrace is how long after the time on its runlease-expires-at label a
// running machine still counts as within its lease. The label is stamped
// before the create request is sent, but the machine counts its lease from
// its first boot and then needs a moment to delete itself, so for a minute or
// two after the label it is rightly still there. The margin also covers a
// clock on this computer that runs ahead of the cloud's.
const leaseGrace = 5 * time.Minute

// Why an untracked instance is reported.
const (
	// Powered off, not deleted, and past the lease on its label: no compute
	// cost, but its disk still bills and nothing will ever remove it. This is
	// the only kind `down --leaked` deletes.
	problemLeaked = "leaked"
	// Powered off or suspended, but not past its lease (or with no lease on
	// record). Its disk bills too, but it may be someone's on purpose.
	problemStopped = "stopped"
	// Running past the lease on its label. That is not proof it is overdue:
	// `runlease snooze` extends the lease on the machine without changing the
	// label, so it may be well within a snoozed lease. Reported, never deleted.
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
	// Two scopes can be the same place: a project recorded in the registry and
	// the default project that an entry or the config leaves unnamed. A machine
	// found through both is one machine.
	seen := map[string]bool{}
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
				key := fmt.Sprintf("%s|%s|%s|%s", m.Provider, m.Project, m.Region, m.ID)
				if seen[key] {
					continue
				}
				seen[key] = true
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
	pastLease := m.ExpiresAt != nil && now.After(m.ExpiresAt.Add(leaseGrace))
	switch {
	case m.State == provider.StatusStopped && pastLease:
		return problemLeaked
	case m.State == provider.StatusStopped:
		return problemStopped
	case pastLease:
		return problemExpired
	default:
		return problemElsewhere
	}
}

func problemRank(p string) int {
	switch p {
	case problemLeaked:
		return 0
	case problemStopped:
		return 1
	case problemExpired:
		return 2
	default:
		return 3
	}
}

// removable reports whether `down --leaked` may delete the instance. Only one
// kind qualifies: stopped, and past the lease on its label. A stopped machine
// cannot be mid-lease in any way that matters once that time has gone, and it
// cannot have been snoozed into the future, because a snoozed machine runs.
//
// A running machine is never removable here, whatever its label says: the
// label is not updated by `runlease snooze`, so it cannot prove the machine
// is overdue.
func (u untrackedInstance) removable() bool {
	return u.Problem == problemLeaked
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
		if others := len(found) - removable; others > 0 && removable > 0 {
			fmt.Fprintf(out, "   The other %d are left alone by --leaked: a running machine, or a stopped one still within its lease, may be someone's.\n", others)
		}
	}
	for _, n := range notes {
		fmt.Fprintf(out, "\nnote: %s\n", n)
	}
}

func explain(u untrackedInstance) string {
	switch u.Problem {
	case problemLeaked:
		return "stopped, not deleted, lease over: its disk still bills"
	case problemStopped:
		if u.ExpiresAt == nil {
			return "stopped, no lease on record: its disk still bills"
		}
		return "stopped within its lease: its disk still bills"
	case problemExpired:
		return "running " + fmtDuration(time.Since(*u.ExpiresAt).Round(time.Minute)) + " past its original lease (snoozed?)"
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
