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
}

func (c *cloudFake) ListManaged(context.Context) ([]provider.ManagedInstance, error) {
	return c.managed, c.listErr
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

// --leaked deletes what should not exist, and leaves a live machine alone
// even though this computer does not track it.
func TestDownLeaked(t *testing.T) {
	cloud := &cloudFake{managed: []provider.ManagedInstance{
		managed("rl-leak-bbbbbb", provider.StatusStopped, -time.Hour),
		managed("rl-overdue-cccccc", provider.StatusRunning, -time.Hour),
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
	if !got["rl-leak-bbbbbb"] || !got["rl-overdue-cccccc"] {
		t.Errorf("leaked and overdue machines must be deleted, deleted: %v", cloud.terminated)
	}
	if got["rl-colleague-dddddd"] || got["rl-mine-eeeeee"] {
		t.Errorf("a live or tracked machine was deleted: %v", cloud.terminated)
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
