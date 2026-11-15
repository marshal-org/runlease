package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

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
	if got := scanScopes(reg); len(got) != 1 {
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

func TestResolveScopes(t *testing.T) {
	names := func(s []cloudScope) string { return strings.Join(scopeNames(s), ", ") }
	tests := []struct {
		name         string
		prov, region string
		gcp, aws     bool
		tracked      bool
		want         string
		wantWhy      bool
		wantErr      string
	}{
		{name: "nothing to go on", want: ""},
		{name: "falls back to the clouds signed in here", gcp: true, aws: true, want: "gcp (default project), aws eu-north-1", wantWhy: true},
		{name: "falls back to the one cloud signed in", aws: true, want: "aws eu-north-1", wantWhy: true},
		{name: "a tracked machine says where, so no fallback", gcp: true, aws: true, tracked: true, want: "gcp project proj"},
		{name: "flags win over everything", prov: "aws", region: "us-west-2", gcp: true, tracked: true, want: "aws us-west-2"},
		{name: "provider alone takes its default region", prov: "aws", want: "aws eu-north-1"},
		{name: "region alone names its cloud", region: "us-west-2", want: "aws us-west-2"},
		{name: "a zone alone names Google Cloud", region: "europe-west1-b", want: "gcp (default project)"},
		{name: "a region of the other cloud", prov: "aws", region: "us-central1-a", wantErr: "does not fit aws"},
		{name: "not a region at all", region: "mars", wantErr: "not a region of either cloud"},
		{name: "unknown provider", prov: "azure", wantErr: "unknown provider"},
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
			scopes, why, err := resolveScopes(reg, tc.prov, tc.region, true)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := names(scopes); got != tc.want || (why != "") != tc.wantWhy {
				t.Errorf("scopes = %q (why %q), want %q, a reason: %v", got, why, tc.want, tc.wantWhy)
			}
		})
	}
}

// Without detect (a plain `list`), nowhere known means nothing is asked.
func TestResolveScopes_noFallbackUnlessAsked(t *testing.T) {
	reg := untrackedHarness(t, &cloudFake{})
	setUp(t, true, true, "eu-north-1")
	if scopes, _, err := resolveScopes(reg, "", "", false); err != nil || len(scopes) != 0 {
		t.Errorf("scopes = %v, err = %v; want none", scopes, err)
	}
}

// The bug: nothing tracked and no default saved used to end in "nowhere to
// look", with a leaked machine sitting in the cloud this computer is signed
// in to.
func TestDownLeaked_fallsBackToTheCloudSignedIn(t *testing.T) {
	cloud := &cloudFake{managed: []provider.ManagedInstance{managed("rl-leak-bbbbbb", provider.StatusStopped, -time.Hour)}}
	reg := untrackedHarness(t, cloud)
	setUp(t, true, false, "")
	out, err := runLeaked(t, reg)
	if err != nil {
		t.Fatalf("runDownLeaked: %v\n%s", err, out)
	}
	if len(cloud.terminated) != 1 || cloud.terminated[0] != "rl-leak-bbbbbb" {
		t.Errorf("deleted %v, want the leaked machine", cloud.terminated)
	}
	if !strings.Contains(out, "Looking in gcp (default project)") || !strings.Contains(out, "--provider and --region") {
		t.Errorf("output does not say where it looked:\n%s", out)
	}
}

func TestDownLeaked_flagsSayWhere(t *testing.T) {
	cloud := &cloudFake{}
	reg := untrackedHarness(t, cloud)
	setUp(t, false, false, "")
	var asked []string
	newProvider = func(_ context.Context, pt provider.ProviderType, region, _ string) (provider.CloudProvider, error) {
		asked = append(asked, string(pt)+" "+region)
		return cloud, nil
	}
	downProvider, downRegion = "aws", "eu-north-1"
	t.Cleanup(func() { downProvider, downRegion = "", "" })
	out, err := runLeaked(t, reg)
	if err != nil || !strings.Contains(out, "Nothing leaked") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if len(asked) != 1 || asked[0] != "aws eu-north-1" {
		t.Errorf("asked %v, want aws eu-north-1", asked)
	}
	if strings.Contains(out, "Looking in") {
		t.Errorf("a place the user chose needs no announcement:\n%s", out)
	}
}

// Still nowhere to look: the error names the flags that would say where.
func TestDownLeaked_nowhereToLookNamesTheFlags(t *testing.T) {
	reg := untrackedHarness(t, &cloudFake{})
	setUp(t, false, false, "")
	_, err := runLeaked(t, reg)
	if err == nil || !strings.Contains(err.Error(), "--provider") || !strings.Contains(err.Error(), "--region") {
		t.Errorf("err = %v, want it to name --provider and --region", err)
	}
}
