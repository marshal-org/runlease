package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	rlconfig "github.com/marshal-org/runlease/pkg/config"
	"github.com/marshal-org/runlease/pkg/provider"
	"github.com/marshal-org/runlease/pkg/registry"
)

// cloudFake is a cloud that holds the given runlease machines.
type cloudFake struct {
	fakeDownProvider
	managed    []provider.ManagedInstance
	listErr    error
	terminated []provider.InstanceID
	// stateNow overrides what Status reports for a machine, for one whose
	// state changed after it was listed.
	stateNow map[provider.InstanceID]provider.InstanceStatus
}

func (c *cloudFake) ListManaged(context.Context) ([]provider.ManagedInstance, error) {
	return c.managed, c.listErr
}

func (c *cloudFake) Status(_ context.Context, id provider.InstanceID) (provider.InstanceStatus, error) {
	if s, ok := c.stateNow[id]; ok {
		return s, nil
	}
	for _, m := range c.managed {
		if m.ID == id {
			return m.State, nil
		}
	}
	return provider.StatusTerminated, nil
}

func (c *cloudFake) Terminate(_ context.Context, id provider.InstanceID) error {
	c.terminated = append(c.terminated, id)
	return nil
}

func untrackedHarness(t *testing.T, cloud *cloudFake) *registry.Registry {
	t.Helper()
	t.Setenv("RUNLEASE_HOME", t.TempDir())
	orig := newProvider
	t.Cleanup(func() { newProvider = orig })
	newProvider = func(context.Context, provider.ProviderType, string, string) (provider.CloudProvider, error) {
		return cloud, nil
	}
	reg, err := loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func managed(id string, state provider.InstanceStatus, expires time.Duration) provider.ManagedInstance {
	e := time.Now().Add(expires)
	return provider.ManagedInstance{
		ID: provider.InstanceID(id), Provider: provider.ProviderGCP, Project: "proj",
		Region: "europe-west1-b", State: state, ExpiresAt: &e, DiskGB: 50, DiskMonthlyUSD: 2,
		CreatedAt: time.Now().Add(-48 * time.Hour),
	}
}

var gcpScope = []cloudScope{{provider: provider.ProviderGCP, region: "europe-west1-b", project: "proj"}}

func TestFindUntracked(t *testing.T) {
	cloud := &cloudFake{managed: []provider.ManagedInstance{
		managed("rl-tracked-aaaaaa", provider.StatusRunning, time.Hour),
		managed("rl-leak-bbbbbb", provider.StatusStopped, -time.Hour),
		managed("rl-overdue-cccccc", provider.StatusRunning, -time.Hour),
		managed("rl-colleague-dddddd", provider.StatusRunning, time.Hour),
	}}
	reg := untrackedHarness(t, cloud)
	if err := reg.Add(trackedEntry(t, "tracked", "rl-tracked-aaaaaa")); err != nil {
		t.Fatal(err)
	}

	found, notes := findUntracked(context.Background(), reg, gcpScope)
	if len(notes) != 0 {
		t.Fatalf("unexpected notes: %v", notes)
	}
	got := map[string]string{}
	for _, u := range found {
		got[string(u.ID)] = u.Problem
	}
	want := map[string]string{
		"rl-leak-bbbbbb":      problemLeaked,    // stopped: its disk still bills
		"rl-overdue-cccccc":   problemExpired,   // running past its lease
		"rl-colleague-dddddd": problemElsewhere, // someone's live machine
	}
	if len(got) != len(want) {
		t.Fatalf("found %v, want %v (the tracked machine must not be reported)", got, want)
	}
	for id, problem := range want {
		if got[id] != problem {
			t.Errorf("%s: problem = %q, want %q", id, got[id], problem)
		}
	}
	if found[0].Problem != problemLeaked {
		t.Errorf("leaked machines should be listed first, got %q first", found[0].Problem)
	}
}

// A cloud that cannot be asked becomes a note. `list` must still work.
func TestFindUntracked_unreachableCloudIsANote(t *testing.T) {
	reg := untrackedHarness(t, &cloudFake{listErr: context.DeadlineExceeded})
	found, notes := findUntracked(context.Background(), reg, gcpScope)
	if len(found) != 0 || len(notes) != 1 || !strings.Contains(notes[0], "could not check gcp project proj") {
		t.Errorf("found=%v notes=%v, want one note and nothing found", found, notes)
	}
}

// The case from the bug report: the registry is empty, a stopped machine is
// still holding a disk, and list used to say "No active instances."
func TestList_showsLeakWhenRegistryIsEmpty(t *testing.T) {
	cloud := &cloudFake{managed: []provider.ManagedInstance{managed("rl-leak-bbbbbb", provider.StatusStopped, -time.Hour)}}
	reg := untrackedHarness(t, cloud)
	found, notes := findUntracked(context.Background(), reg, gcpScope)
	var out bytes.Buffer
	printUntracked(&out, found, notes)
	for _, want := range []string{"rl-leak-bbbbbb", "50 GB", "its disk still bills", "runlease down --leaked", "$2.00 a month"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}

// --leaked deletes a stopped machine whose lease is over, and nothing else.
// In particular it never deletes a running machine: a machine running past
// its label may have been snoozed, which extends the lease on the machine
// without changing the label.
func TestDownLeaked(t *testing.T) {
	cloud := &cloudFake{managed: []provider.ManagedInstance{
		managed("rl-leak-bbbbbb", provider.StatusStopped, -time.Hour),
		managed("rl-overdue-cccccc", provider.StatusRunning, -time.Hour), // snoozed, perhaps
		managed("rl-paused-ffffff", provider.StatusStopped, time.Hour),   // stopped within its lease
		managed("rl-colleague-dddddd", provider.StatusRunning, time.Hour),
	}}
	reg := untrackedHarness(t, cloud)
	// Something to derive a scope from: a tracked machine in the same project.
	if err := reg.Add(trackedEntry(t, "mine", "rl-mine-eeeeee")); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	downCmd.SetOut(&out)
	downCmd.SetErr(&out)
	downCmd.SetContext(context.Background())
	downYes = true
	t.Cleanup(func() { downYes = false })
	if err := runDownLeaked(downCmd, reg); err != nil {
		t.Fatalf("runDownLeaked: %v\n%s", err, out.String())
	}
	got := map[provider.InstanceID]bool{}
	for _, id := range cloud.terminated {
		got[id] = true
	}
	if !got["rl-leak-bbbbbb"] {
		t.Errorf("the stopped machine past its lease must be deleted, deleted: %v", cloud.terminated)
	}
	for _, keep := range []provider.InstanceID{"rl-overdue-cccccc", "rl-paused-ffffff", "rl-colleague-dddddd", "rl-mine-eeeeee"} {
		if got[keep] {
			t.Errorf("%s was deleted; only a stopped machine past its lease may be", keep)
		}
	}
}

// runLeaked runs `down --leaked --yes` and returns what it printed.
func runLeaked(t *testing.T, reg *registry.Registry) (string, error) {
	t.Helper()
	var out bytes.Buffer
	downCmd.SetOut(&out)
	downCmd.SetErr(&out)
	downCmd.SetContext(context.Background())
	downYes = true
	t.Cleanup(func() { downYes = false })
	err := runDownLeaked(downCmd, reg)
	return out.String(), err
}

// The label is stamped before the machine exists, and the machine counts its
// lease from first boot: just past the label it is still within its lease.
func TestClassify_graceAfterTheLabel(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		past time.Duration
		want string
	}{
		{-time.Minute, problemElsewhere},
		{time.Minute, problemElsewhere},
		{leaseGrace + time.Minute, problemExpired},
	} {
		m := managed("rl-x-aaaaaa", provider.StatusRunning, -tc.past)
		if got := classify(m, now); got != tc.want {
			t.Errorf("running, %s past its label: %q, want %q", tc.past, got, tc.want)
		}
	}
}

// The registry names a project and the config leaves the default one
// unnamed: when they are the same project, its machines are found twice.
func TestFindUntracked_sameProjectReachedTwice(t *testing.T) {
	cloud := &cloudFake{managed: []provider.ManagedInstance{managed("rl-leak-bbbbbb", provider.StatusStopped, -time.Hour)}}
	reg := untrackedHarness(t, cloud)
	scopes := append([]cloudScope{{provider: provider.ProviderGCP, region: "europe-west1-b"}}, gcpScope...)
	found, _ := findUntracked(context.Background(), reg, scopes)
	if len(found) != 1 {
		t.Errorf("found %d rows for one machine: %v", len(found), found)
	}
}

// A cloud that could not be asked must not come back as "nothing leaked".
func TestDownLeaked_unreachableCloudIsNotAnAllClear(t *testing.T) {
	reg := untrackedHarness(t, &cloudFake{listErr: context.DeadlineExceeded})
	if err := reg.Add(trackedEntry(t, "mine", "rl-mine-eeeeee")); err != nil {
		t.Fatal(err)
	}
	out, err := runLeaked(t, reg)
	if err == nil || strings.Contains(out, "Nothing leaked") {
		t.Errorf("err = %v, output:\n%s\nwant an error and no all-clear", err, out)
	}
}

// A machine listed as stopped and started again before the delete is someone's
// live machine by then.
func TestDownLeaked_leavesAMachineStartedSince(t *testing.T) {
	cloud := &cloudFake{
		managed: []provider.ManagedInstance{
			managed("rl-leak-bbbbbb", provider.StatusStopped, -time.Hour),
			managed("rl-back-ffffff", provider.StatusStopped, -time.Hour),
		},
		stateNow: map[provider.InstanceID]provider.InstanceStatus{"rl-back-ffffff": provider.StatusRunning},
	}
	reg := untrackedHarness(t, cloud)
	if err := reg.Add(trackedEntry(t, "mine", "rl-mine-eeeeee")); err != nil {
		t.Fatal(err)
	}
	out, err := runLeaked(t, reg)
	if err != nil {
		t.Fatalf("runDownLeaked: %v\n%s", err, out)
	}
	if len(cloud.terminated) != 1 || cloud.terminated[0] != "rl-leak-bbbbbb" {
		t.Errorf("deleted %v, want only the machine that is still stopped\n%s", cloud.terminated, out)
	}
	if !strings.Contains(out, "rl-back-ffffff left alone") {
		t.Errorf("output does not say the restarted machine was left alone:\n%s", out)
	}
}

func TestScanScopes_oneListingPerProject(t *testing.T) {
	reg := untrackedHarness(t, &cloudFake{})
	a := trackedEntry(t, "a", "rl-a-aaaaaa")
	b := trackedEntry(t, "b", "rl-b-bbbbbb")
	b.Handle.Region = "us-central1-a" // another zone, same project
	_ = reg.Add(a)
	_ = reg.Add(b)
	if got := scanScopes(reg, nil); len(got) != 1 {
		t.Errorf("scopes = %v, want one: a project listing covers every zone", got)
	}
}

// Every combination of state and lease, and which one may be deleted.
func TestClassify_onlyStoppedPastLeaseIsRemovable(t *testing.T) {
	now := time.Now()
	noLease := managed("rl-x-aaaaaa", provider.StatusStopped, time.Hour)
	noLease.ExpiresAt = nil
	for _, tc := range []struct {
		name      string
		m         provider.ManagedInstance
		problem   string
		removable bool
	}{
		{"stopped, lease over", managed("rl-x-aaaaaa", provider.StatusStopped, -time.Hour), problemLeaked, true},
		{"stopped, within lease", managed("rl-x-aaaaaa", provider.StatusStopped, time.Hour), problemStopped, false},
		{"stopped, no lease on record", noLease, problemStopped, false},
		{"stopped, just past the label", managed("rl-x-aaaaaa", provider.StatusStopped, -time.Minute), problemStopped, false},
		{"running, past the label", managed("rl-x-aaaaaa", provider.StatusRunning, -time.Hour), problemExpired, false},
		{"running, within lease", managed("rl-x-aaaaaa", provider.StatusRunning, time.Hour), problemElsewhere, false},
		{"pending, past the label", managed("rl-x-aaaaaa", provider.StatusPending, -time.Hour), problemExpired, false},
	} {
		u := untrackedInstance{ManagedInstance: tc.m, Problem: classify(tc.m, now)}
		if u.Problem != tc.problem || u.removable() != tc.removable {
			t.Errorf("%s: problem=%q removable=%v, want %q %v", tc.name, u.Problem, u.removable(), tc.problem, tc.removable)
		}
	}
}

// setUp says which clouds look signed in, and what region the AWS setup names.
func setUp(t *testing.T, gcp, aws bool, awsRegion string) {
	t.Helper()
	origClouds, origRegion := cloudsSetUp, configuredAWSRegion
	t.Cleanup(func() { cloudsSetUp, configuredAWSRegion = origClouds, origRegion })
	cloudsSetUp = func() (bool, bool) { return gcp, aws }
	configuredAWSRegion = func(context.Context) string { return awsRegion }
}

// leakedFlags sets --provider, --region and --project for one test.
func leakedFlags(t *testing.T, prov, region, project string) {
	t.Helper()
	downProvider, downRegion, downProject = prov, region, project
	t.Cleanup(func() { downProvider, downRegion, downProject = "", "", "" })
}

func TestResolveScopes(t *testing.T) {
	saved := &rlconfig.Config{Defaults: rlconfig.InstanceConfig{Provider: "aws", Region: "eu-west-1"}}
	tests := []struct {
		name        string
		f           scopeFlags
		cfg         *rlconfig.Config
		gcp, aws    bool
		tracked     bool
		want        string // after the project and account are looked up
		wantUnnamed bool
		wantSkipped string
		wantErr     string
	}{
		{name: "nothing to go on", want: ""},
		{name: "falls back to the clouds signed in here", gcp: true, aws: true,
			want: "gcp project env-project (all zones); aws eu-north-1 (account env-project)", wantUnnamed: true},
		{name: "a tracked machine says where: named, no fallback", gcp: true, aws: true, tracked: true, want: "gcp project proj (all zones)"},
		{name: "a saved default says where: named", cfg: saved, gcp: true, want: "aws eu-west-1 (account env-project)"},

		{name: "AWS region given: named", f: scopeFlags{provider: "aws", region: "us-west-2"}, want: "aws us-west-2 (account env-project)"},
		{name: "AWS region alone names its cloud", f: scopeFlags{region: "us-west-2"}, want: "aws us-west-2 (account env-project)"},
		{name: "AWS with no region: filled in, so not named", f: scopeFlags{provider: "aws"}, want: "aws eu-north-1 (account env-project)", wantUnnamed: true},
		{name: "GCP project given: named", f: scopeFlags{provider: "gcp", project: "p1"}, want: "gcp project p1 (all zones)"},
		{name: "--project alone is Google Cloud", f: scopeFlags{project: "p1"}, want: "gcp project p1 (all zones)"},
		{name: "GCP with no project: filled in, so not named", f: scopeFlags{provider: "gcp"}, want: "gcp project env-project (all zones)", wantUnnamed: true},
		{name: "a zone does not narrow Google Cloud and does not name the project", f: scopeFlags{region: "europe-west1-b"},
			want: "gcp project env-project (all zones)", wantUnnamed: true},

		{name: "flags leave a known place out, and say so", f: scopeFlags{provider: "aws", region: "us-west-2"}, tracked: true,
			want: "aws us-west-2 (account env-project)", wantSkipped: "gcp project proj (all zones)"},
		{name: "flags naming the known place skip nothing", f: scopeFlags{project: "proj"}, tracked: true, want: "gcp project proj (all zones)"},

		{name: "a region of the other cloud", f: scopeFlags{provider: "aws", region: "us-central1-a"}, wantErr: "does not fit aws"},
		{name: "not a region at all", f: scopeFlags{region: "mars"}, wantErr: "not a region of either cloud"},
		{name: "unknown provider", f: scopeFlags{provider: "azure"}, wantErr: "unknown provider"},
		{name: "a project on AWS", f: scopeFlags{provider: "aws", project: "p1"}, wantErr: "--project is only for Google Cloud"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reg := untrackedHarness(t, &cloudFake{})
			setUp(t, tc.gcp, tc.aws, "eu-north-1")
			if tc.tracked {
				if err := reg.Add(trackedEntry(t, "mine", "rl-mine-eeeeee")); err != nil {
					t.Fatal(err)
				}
			}
			plan, err := resolveScopes(reg, tc.cfg, tc.f, true)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			plan = locateScopes(context.Background(), plan)
			if got := strings.Join(scopeNames(plan.scopes), "; "); got != tc.want {
				t.Errorf("scopes = %q, want %q", got, tc.want)
			}
			if (plan.unnamed != "") != tc.wantUnnamed {
				t.Errorf("unnamed = %q, want set: %v", plan.unnamed, tc.wantUnnamed)
			}
			if got := strings.Join(scopeNames(plan.skipped), "; "); got != tc.wantSkipped {
				t.Errorf("skipped = %q, want %q", got, tc.wantSkipped)
			}
		})
	}
}

// Without detect (a plain `list`), nowhere known means nothing is asked.
func TestResolveScopes_noFallbackUnlessAsked(t *testing.T) {
	reg := untrackedHarness(t, &cloudFake{})
	setUp(t, true, true, "eu-north-1")
	if plan, err := resolveScopes(reg, nil, scopeFlags{}, false); err != nil || len(plan.scopes) != 0 {
		t.Errorf("scopes = %v, err = %v; want none", plan.scopes, err)
	}
}

// The place is printed by its real name before anything else happens, on
// every path: here the registry's, which nobody had to guess.
func TestDownLeaked_saysWhereBeforeDeleting(t *testing.T) {
	cloud := &cloudFake{managed: []provider.ManagedInstance{managed("rl-leak-bbbbbb", provider.StatusStopped, -time.Hour)}}
	reg := untrackedHarness(t, cloud)
	if err := reg.Add(trackedEntry(t, "mine", "rl-mine-eeeeee")); err != nil {
		t.Fatal(err)
	}
	out, err := runLeaked(t, reg)
	if err != nil {
		t.Fatalf("runDownLeaked: %v\n%s", err, out)
	}
	where, deleted := strings.Index(out, "Looking in: gcp project proj (all zones)"), strings.Index(out, "rl-leak-bbbbbb deleted")
	if where < 0 || deleted < 0 || where > deleted {
		t.Errorf("want the place named before the delete:\n%s", out)
	}
	if strings.Contains(out, "default project") {
		t.Errorf("the project must be named, never 'default project':\n%s", out)
	}
}

// The bug: nothing tracked and no default saved used to end in "nowhere to
// look". Now the clouds signed in here are looked in, by name. Asked, a
// person may confirm the delete.
func TestDownLeaked_fallbackAsksAndThenDeletes(t *testing.T) {
	cloud := &cloudFake{managed: []provider.ManagedInstance{managed("rl-leak-bbbbbb", provider.StatusStopped, -time.Hour)}}
	reg := untrackedHarness(t, cloud)
	setUp(t, true, false, "")
	var out bytes.Buffer
	downCmd.SetOut(&out)
	downCmd.SetErr(&out)
	downCmd.SetContext(context.Background())

	downCmd.SetIn(strings.NewReader("n\n"))
	if err := runDownLeaked(downCmd, reg); err != nil || len(cloud.terminated) != 0 {
		t.Fatalf("answered no: err = %v, deleted %v\n%s", err, cloud.terminated, out.String())
	}
	asked := strings.Index(out.String(), "Delete them?")
	where := strings.Index(out.String(), "Looking in: gcp project env-project (all zones)")
	if where < 0 || asked < where || !strings.Contains(out.String(), "every cloud signed in here") {
		t.Errorf("the place and why must be printed before the question:\n%s", out.String())
	}

	downCmd.SetIn(strings.NewReader("y\n"))
	t.Cleanup(func() { downCmd.SetIn(nil) })
	if err := runDownLeaked(downCmd, reg); err != nil {
		t.Fatal(err)
	}
	if len(cloud.terminated) != 1 || cloud.terminated[0] != "rl-leak-bbbbbb" {
		t.Errorf("answered yes: deleted %v, want the leaked machine", cloud.terminated)
	}
}

// --yes confirms nothing in a place nobody named: the machines are listed,
// none is deleted, and the error gives the command that names the place.
func TestDownLeaked_refusesToDeleteUnattendedInAPlaceNobodyNamed(t *testing.T) {
	leak := []provider.ManagedInstance{managed("rl-leak-bbbbbb", provider.StatusStopped, -time.Hour)}
	tests := []struct {
		name                  string
		prov, region, project string
		gcp, aws              bool
		wantCmd               string
	}{
		{name: "fallback to the cloud signed in", gcp: true, wantCmd: "runlease down --leaked --yes --provider gcp --project env-project"},
		{name: "--provider gcp, project filled in", prov: "gcp", wantCmd: "runlease down --leaked --yes --provider gcp --project env-project"},
		{name: "a zone, project filled in", region: "us-central1-a", wantCmd: "--provider gcp --project env-project"},
		{name: "--provider aws, region filled in", prov: "aws", wantCmd: "runlease down --leaked --yes --provider aws --region eu-north-1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cloud := &cloudFake{managed: leak}
			reg := untrackedHarness(t, cloud)
			setUp(t, tc.gcp, tc.aws, "eu-north-1")
			leakedFlags(t, tc.prov, tc.region, tc.project)
			out, err := runLeaked(t, reg) // --yes
			if err == nil || !strings.Contains(err.Error(), "nothing was deleted") || !strings.Contains(err.Error(), tc.wantCmd) {
				t.Fatalf("err = %v\nwant a refusal naming %q\n%s", err, tc.wantCmd, out)
			}
			if len(cloud.terminated) != 0 {
				t.Errorf("deleted %v unattended in a place nobody named", cloud.terminated)
			}
			if !strings.Contains(out, "rl-leak-bbbbbb") || !strings.Contains(out, "Looking in:") {
				t.Errorf("the place and the machines found should still be shown:\n%s", out)
			}
		})
	}
}

// With the place named in full, --yes deletes as before.
func TestDownLeaked_deletesUnattendedInANamedPlace(t *testing.T) {
	for _, tc := range []struct{ name, prov, region, project, asked string }{
		{"AWS region", "aws", "eu-north-1", "", "aws eu-north-1 "},
		{"GCP project", "gcp", "", "p1", "gcp us-central1-a p1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cloud := &cloudFake{managed: []provider.ManagedInstance{managed("rl-leak-bbbbbb", provider.StatusStopped, -time.Hour)}}
			reg := untrackedHarness(t, cloud)
			setUp(t, false, false, "")
			var asked []string
			newProvider = func(_ context.Context, pt provider.ProviderType, region, project string) (provider.CloudProvider, error) {
				asked = append(asked, string(pt)+" "+region+" "+project)
				return cloud, nil
			}
			leakedFlags(t, tc.prov, tc.region, tc.project)
			out, err := runLeaked(t, reg)
			if err != nil || len(cloud.terminated) != 1 {
				t.Fatalf("err = %v, deleted %v\n%s", err, cloud.terminated, out)
			}
			for _, a := range asked {
				// The delete itself goes to where the fake machine says it lives.
				if a != tc.asked && a != "gcp europe-west1-b proj" {
					t.Errorf("asked %q, want only %q", a, tc.asked)
				}
			}
		})
	}
}

// In JSON the refusal still prints one document: what was found, not deleted.
func TestDownLeaked_JSONRefusalListsWhatItLeft(t *testing.T) {
	jsonMode(t)
	cloud := &cloudFake{managed: []provider.ManagedInstance{managed("rl-leak-bbbbbb", provider.StatusStopped, -time.Hour)}}
	reg := untrackedHarness(t, cloud)
	setUp(t, true, false, "")
	stdout, stderr := capture(downCmd)
	downYes = true
	t.Cleanup(func() { downYes = false })
	err := runDownLeaked(downCmd, reg)
	if err == nil || len(cloud.terminated) != 0 {
		t.Fatalf("err = %v, deleted %v", err, cloud.terminated)
	}
	var got leakedJSON
	decode(t, stdout, &got)
	if len(got.Deleted) != 0 || len(got.Kept) != 1 || got.Kept[0].ID != "rl-leak-bbbbbb" || !strings.Contains(got.Kept[0].Reason, "not named") {
		t.Errorf("got %+v", got)
	}
	if len(got.Scopes) != 1 || got.Scopes[0] != "gcp project env-project (all zones)" {
		t.Errorf("scopes = %v", got.Scopes)
	}
	if !strings.Contains(stderr.String(), "Looking in: gcp project env-project (all zones)") {
		t.Errorf("stderr does not say where: %s", stderr.String())
	}
}

// A config file that cannot be read is not an empty one.
func TestDownLeaked_unreadableConfigIsAnError(t *testing.T) {
	cloud := &cloudFake{managed: []provider.ManagedInstance{managed("rl-leak-bbbbbb", provider.StatusStopped, -time.Hour)}}
	reg := untrackedHarness(t, cloud)
	setUp(t, true, true, "eu-north-1")
	if err := os.WriteFile(defaultConfigPath(), []byte("defaults: [not, a, map"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runLeaked(t, reg)
	if err == nil || !strings.Contains(err.Error(), "config") || len(cloud.terminated) != 0 || strings.Contains(out, "Looking in") {
		t.Errorf("err = %v, deleted %v; want a config error before anything is looked at\n%s", err, cloud.terminated, out)
	}
}

// Flags that narrow the search say what they leave out.
func TestDownLeaked_flagsSayWhatTheySkip(t *testing.T) {
	reg := untrackedHarness(t, &cloudFake{})
	setUp(t, false, false, "")
	if err := reg.Add(trackedEntry(t, "mine", "rl-mine-eeeeee")); err != nil {
		t.Fatal(err)
	}
	leakedFlags(t, "aws", "eu-north-1", "")
	out, err := runLeaked(t, reg)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{"Looking in: aws eu-north-1 (account env-project)", "Not looking in: gcp project proj (all zones)"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// Still nowhere to look: the error names the flags that would say where.
func TestDownLeaked_nowhereToLookNamesTheFlags(t *testing.T) {
	reg := untrackedHarness(t, &cloudFake{})
	setUp(t, false, false, "")
	_, err := runLeaked(t, reg)
	for _, flag := range []string{"--provider", "--region", "--project"} {
		if err == nil || !strings.Contains(err.Error(), flag) {
			t.Errorf("err = %v, want it to name %s", err, flag)
		}
	}
}

// runListWith runs `list` with the given flags set and returns both streams.
func runListWith(t *testing.T, set func()) (stdout, stderr string, err error) {
	t.Helper()
	reset := func() { listLocal, listUntracked, listProvider, listRegion, listProject = false, false, "", "", "" }
	reset()
	t.Cleanup(reset)
	set()
	o, e := capture(listCmd)
	err = runList(listCmd, nil)
	return o.String(), e.String(), err
}

// As before the flags existed: nowhere to look is an empty list, not an error.
func TestList_JSONUntrackedWithNowhereToLook(t *testing.T) {
	jsonMode(t)
	untrackedHarness(t, &cloudFake{})
	setUp(t, false, false, "")
	stdout, stderr, err := runListWith(t, func() { listUntracked = true })
	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	if strings.TrimSpace(stdout) != "[]" {
		t.Errorf("stdout = %q, want []", stdout)
	}
	if !strings.Contains(stderr, "nowhere to look") {
		t.Errorf("stderr = %q, want a note", stderr)
	}
}

// With a cloud signed in, --untracked finds what is there and says where.
func TestList_JSONUntrackedFallsBack(t *testing.T) {
	jsonMode(t)
	untrackedHarness(t, &cloudFake{managed: []provider.ManagedInstance{managed("rl-leak-bbbbbb", provider.StatusStopped, -time.Hour)}})
	setUp(t, true, false, "")
	stdout, stderr, err := runListWith(t, func() { listUntracked = true })
	if err != nil || !strings.Contains(stdout, `"rl-leak-bbbbbb"`) || !strings.Contains(stderr, "Looking in: gcp project env-project (all zones)") {
		t.Errorf("err = %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
}

// A flag that would do nothing is refused, not ignored.
func TestList_whereFlagsWithNoEffect(t *testing.T) {
	untrackedHarness(t, &cloudFake{})
	setUp(t, false, false, "")
	if _, _, err := runListWith(t, func() { listLocal, listProvider = true, "aws" }); err == nil || !strings.Contains(err.Error(), "--local") {
		t.Errorf("--local with --provider: err = %v", err)
	}
	jsonMode(t)
	if _, _, err := runListWith(t, func() { listRegion = "eu-north-1" }); err == nil || !strings.Contains(err.Error(), "--untracked") {
		t.Errorf("--output json with --region and no --untracked: err = %v", err)
	}
	stdout, stderr, err := runListWith(t, func() { listUntracked, listRegion = true, "eu-north-1" })
	if err != nil || strings.TrimSpace(stdout) != "[]" || !strings.Contains(stderr, "Looking in: aws eu-north-1") {
		t.Errorf("with --untracked: err = %v, stdout %q, stderr %q", err, stdout, stderr)
	}
}
