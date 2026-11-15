package main

import (
	"context"
	"errors"
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
	project  string // GCP only; "" means the project the environment names, not yet looked up
	account  string // AWS only, and only for display: the account ID, if known
}

func (s cloudScope) String() string {
	if s.provider == provider.ProviderGCP {
		if s.project == "" {
			return "gcp (the project your Google Cloud setup names; it could not be looked up)"
		}
		return "gcp project " + s.project + " (all zones)"
	}
	if s.account != "" {
		return fmt.Sprintf("%s %s (account %s)", s.provider, s.region, s.account)
	}
	return fmt.Sprintf("%s %s", s.provider, s.region)
}

// key names the place, so that the same place reached twice is scanned once.
func (s cloudScope) key() string {
	if s.provider == provider.ProviderGCP {
		return "gcp|" + s.project // one listing covers every zone
	}
	return string(s.provider) + "|" + s.region
}

// flags is the command line that would name this place.
func (s cloudScope) flags() string {
	if s.provider == provider.ProviderGCP {
		project := s.project
		if project == "" {
			project = "PROJECT_ID"
		}
		return "--provider gcp --project " + project
	}
	return fmt.Sprintf("--provider %s --region %s", s.provider, s.region)
}

func dedupeScopes(in []cloudScope) []cloudScope {
	seen := map[string]bool{}
	var out []cloudScope
	for _, s := range in {
		if s.provider == "" || s.region == "" || seen[s.key()] {
			continue
		}
		seen[s.key()] = true
		out = append(out, s)
	}
	return out
}

// scanScopes is where this computer already knows to look: wherever it has
// launched before (the registry), and its saved defaults.
func scanScopes(reg *registry.Registry, cfg *rlconfig.Config) []cloudScope {
	var scopes []cloudScope
	for _, e := range reg.List() {
		scopes = append(scopes, cloudScope{provider: e.Handle.Provider, region: e.Handle.Region, project: e.Handle.Project})
	}
	if cfg != nil {
		scopes = append(scopes, cloudScope{provider: provider.ProviderType(cfg.Defaults.Provider), region: cfg.Defaults.Region})
	}
	return dedupeScopes(scopes)
}

// cloudsSetUp reports which clouds look signed in on this computer. A variable
// so a test does not depend on the computer it runs on.
var cloudsSetUp = func() (gcp, aws bool) { return hasGCPSetup(), hasAWSSetup() }

// applicableClouds is the clouds a command with no --provider is about: the
// ones signed in on this computer, and the saved default provider. It is the
// one answer to "which clouds?" for doctor and for the scan of untracked
// machines.
func applicableClouds(cfg *rlconfig.Config) []string {
	gcp, aws := cloudsSetUp()
	if cfg != nil {
		gcp = gcp || cfg.Defaults.Provider == "gcp"
		aws = aws || cfg.Defaults.Provider == "aws"
	}
	var clouds []string
	if gcp {
		clouds = append(clouds, "gcp")
	}
	if aws {
		clouds = append(clouds, "aws")
	}
	return clouds
}

// scopeFlags is what --provider, --region and --project said.
type scopeFlags struct{ provider, region, project string }

func (f scopeFlags) any() bool { return f.provider != "" || f.region != "" || f.project != "" }

// scopePlan is where a scan for untracked machines will look, and how that
// was arrived at.
type scopePlan struct {
	scopes []cloudScope
	// skipped is the places this computer knows of that the flags left out.
	skipped []cloudScope
	// unnamed is set, with the reason, when the user did not name the places
	// and neither the registry nor a saved default did: the fallback to the
	// clouds signed in here, or a flag that left the AWS region or the Google
	// Cloud project to be filled in. Nothing is deleted in such a place
	// without a person confirming it.
	unnamed string
}

// resolveScopes works out where to look for untracked machines.
//
// With flags, it is the one place they name; the half not given is filled in
// (the provider from how the region is written, the region or project from
// that cloud's defaults) and the plan says so. With none, it is wherever
// scanScopes finds. If that is nowhere and detect is set, it falls back to
// each applicable cloud in its default place. No scopes and no error means
// there is nothing to go on.
func resolveScopes(reg *registry.Registry, cfg *rlconfig.Config, f scopeFlags, detect bool) (scopePlan, error) {
	if cfg == nil {
		cfg = &rlconfig.Config{}
	}
	known := scanScopes(reg, cfg)
	if !f.any() {
		if len(known) > 0 || !detect {
			return scopePlan{scopes: known}, nil
		}
		var plan scopePlan
		for _, c := range applicableClouds(cfg) {
			r, _ := defaultRegionFor(c, cfg)
			plan.scopes = append(plan.scopes, cloudScope{provider: provider.ProviderType(c), region: r})
		}
		if len(plan.scopes) > 0 {
			plan.unnamed = "this computer tracks no machines and has no saved default provider and region, so this is every cloud signed in here, in its default place"
		}
		return plan, nil
	}

	prov := f.provider
	if prov == "" {
		switch {
		case f.region == "", gcpLocation.MatchString(f.region):
			prov = "gcp" // --project alone is Google Cloud's too
		case awsLocation.MatchString(f.region):
			prov = "aws"
		default:
			return scopePlan{}, fmt.Errorf("%q is not a region of either cloud: %s, %s", f.region, regionExample("aws"), regionExample("gcp"))
		}
	}
	if prov != "gcp" && prov != "aws" {
		return scopePlan{}, fmt.Errorf("unknown provider %q — supported: aws, gcp", prov)
	}
	if f.project != "" && prov != "gcp" {
		return scopePlan{}, fmt.Errorf("--project is only for Google Cloud; an AWS account is the one you are signed in to")
	}
	if f.region != "" && !regionFits(prov, f.region) {
		return scopePlan{}, fmt.Errorf("%q does not fit %s: %s", f.region, prov, regionExample(prov))
	}
	s := cloudScope{provider: provider.ProviderType(prov), region: f.region, project: f.project}
	if s.region == "" {
		s.region, _ = defaultRegionFor(prov, cfg)
	}
	plan := scopePlan{scopes: []cloudScope{s}, skipped: known}
	switch {
	case prov == "aws" && f.region == "":
		plan.unnamed = "--region was not given, so this is the region AWS commands default to here"
	case prov == "gcp" && f.project == "":
		plan.unnamed = "--project was not given, so this is the project your Google Cloud setup names"
	}
	return plan, nil
}

// locateScopes fills in what a scope leaves to the environment, so that the
// place printed is the place scanned: the Google Cloud project behind "the
// default one", and for display the AWS account. Then it drops, from skipped,
// the places the scan covers after all. What cannot be looked up stays as it
// was; the scan will say why.
func locateScopes(ctx context.Context, plan scopePlan) scopePlan {
	locate := func(in []cloudScope) []cloudScope {
		out := make([]cloudScope, 0, len(in))
		for _, s := range in {
			if (s.provider == provider.ProviderGCP && s.project == "") || s.provider == provider.ProviderAWS {
				lctx, cancel := context.WithTimeout(ctx, untrackedScanTimeout)
				if p, err := newProvider(lctx, s.provider, s.region, s.project); err == nil {
					if n, ok := p.(provider.AccountNamer); ok {
						if s.provider == provider.ProviderGCP {
							s.project = n.AccountName(lctx)
						} else {
							s.account = n.AccountName(lctx)
						}
					}
				}
				cancel()
			}
			out = append(out, s)
		}
		return dedupeScopes(out)
	}
	plan.scopes = locate(plan.scopes)
	looking := map[string]bool{}
	for _, s := range plan.scopes {
		looking[s.key()] = true
	}
	var skipped []cloudScope
	for _, s := range locate(plan.skipped) {
		if !looking[s.key()] {
			skipped = append(skipped, s)
		}
	}
	plan.skipped = skipped
	return plan
}

// announceScopes is the one place that says where a scan looks. It goes to
// stderr, before anything is listed or deleted.
func announceScopes(w io.Writer, plan scopePlan) {
	if len(plan.scopes) == 0 {
		return
	}
	fmt.Fprintf(w, "Looking in: %s\n", strings.Join(scopeNames(plan.scopes), "; "))
	if plan.unnamed != "" {
		fmt.Fprintf(w, "  (%s. Name the place with --provider, --region and --project.)\n", plan.unnamed)
	}
	if len(plan.skipped) > 0 {
		fmt.Fprintf(w, "Not looking in: %s (left out by the flags given; run again without them, or with theirs, to cover it)\n",
			strings.Join(scopeNames(plan.skipped), "; "))
	}
}

// scopeNames is the scopes as text, for a message.
func scopeNames(scopes []cloudScope) []string {
	names := make([]string, 0, len(scopes))
	for _, s := range scopes {
		names = append(names, s.String())
	}
	return names
}

// errNowhereToLook is the error for a scan with nothing to go on. It names the
// flags, because they are the way out.
var errNowhereToLook = errors.New("nowhere to look: this computer tracks no machines, has no default provider and region, and no cloud sign-in was found. Say where with --provider gcp|aws and --region REGION (AWS) or --project PROJECT_ID (Google Cloud), or run 'runlease configure' first")

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
