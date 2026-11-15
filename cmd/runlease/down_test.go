package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/marshal-org/runlease/pkg/provider"
	"github.com/marshal-org/runlease/pkg/registry"
)

type fakeDownProvider struct {
	terminateErr error
	terminated   bool
	status       provider.InstanceStatus
}

func (f *fakeDownProvider) RequestSpot(context.Context, provider.InstanceSpec) (*provider.InstanceHandle, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeDownProvider) Terminate(context.Context, provider.InstanceID) error {
	f.terminated = true
	return f.terminateErr
}
func (f *fakeDownProvider) Status(context.Context, provider.InstanceID) (provider.InstanceStatus, error) {
	return f.status, nil
}
func (f *fakeDownProvider) WaitUntilRunning(context.Context, provider.InstanceID) (*provider.InstanceHandle, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeDownProvider) ProjectID() string { return "env-project" }

// AccountName is what the environment's project (or account) resolves to.
func (f *fakeDownProvider) AccountName(context.Context) string { return "env-project" }

// runDownWith runs `down --yes` against a registry holding one GCP entry with
// the given recorded project, and a provider whose Terminate returns termErr.
// It returns the output, the error, the project newProvider was asked for, and
// whether the registry entry survived.
func runDownWith(t *testing.T, project string, termErr error) (out string, err error, gotProject string, kept bool) {
	t.Helper()
	t.Setenv("RUNLEASE_HOME", t.TempDir())
	reg, lerr := loadRegistry()
	if lerr != nil {
		t.Fatalf("loadRegistry: %v", lerr)
	}
	if aerr := reg.Add(&registry.Entry{Handle: provider.InstanceHandle{
		ID: "rl-box-abc123", ManifestName: "box", Provider: provider.ProviderGCP,
		Region: "europe-west1-b", Project: project,
	}, CredDir: t.TempDir()}); aerr != nil {
		t.Fatalf("reg.Add: %v", aerr)
	}

	fake := &fakeDownProvider{terminateErr: termErr}
	orig := newProvider
	t.Cleanup(func() { newProvider = orig })
	newProvider = func(_ context.Context, _ provider.ProviderType, _, p string) (provider.CloudProvider, error) {
		gotProject = p
		return fake, nil
	}

	var buf bytes.Buffer
	downCmd.SetOut(&buf)
	downCmd.SetErr(&buf)
	downCmd.SetContext(context.Background())
	downYes = true
	t.Cleanup(func() { downYes = false })
	err = runDown(downCmd, []string{"box"})

	reg2, _ := loadRegistry()
	_, gerr := reg2.Get("box")
	return buf.String(), err, gotProject, gerr == nil
}

func TestDown_usesRecordedProject(t *testing.T) {
	t.Setenv("GOOGLE_CLOUD_PROJECT", "env-project")
	out, err, got, kept := runDownWith(t, "stored-project", nil)
	if err != nil {
		t.Fatalf("down: %v", err)
	}
	if got != "stored-project" {
		t.Fatalf("provider built for project %q, want stored-project", got)
	}
	if kept || !strings.Contains(out, "Terminated.") {
		t.Fatalf("want entry removed and Terminated printed; kept=%v out=%q", kept, out)
	}
}

func TestDown_terminateFailureKeepsEntry(t *testing.T) {
	out, err, _, kept := runDownWith(t, "proj-a", errors.New("instance still exists"))
	if err == nil || !kept || strings.Contains(out, "Terminated.") {
		t.Fatalf("want error + entry kept + no Terminated; err=%v kept=%v out=%q", err, kept, out)
	}
}

// Legacy entry (no recorded project): a 404 may just mean the wrong project,
// so it must not print success or drop the entry.
func TestDown_notFoundWithoutRecordedProjectKeepsEntry(t *testing.T) {
	nf := fmt.Errorf("instance rl-box-abc123 not found in project env-project, zone europe-west1-b: %w", provider.ErrNotFound)
	out, err, _, kept := runDownWith(t, "", nf)
	if err == nil || !kept || strings.Contains(out, "Terminated.") {
		t.Fatalf("want error + entry kept + no Terminated; err=%v kept=%v out=%q", err, kept, out)
	}
	if !strings.Contains(out, "no recorded GCP project") {
		t.Fatalf("want a legacy-entry warning, got %q", out)
	}
}

// With a recorded project a 404 is checked where the VM was created, so the
// entry goes — but the output says "not found", not "Terminated".
func TestDown_notFoundInRecordedProjectSaysSo(t *testing.T) {
	nf := fmt.Errorf("instance rl-box-abc123 not found in project proj-a, zone europe-west1-b: %w", provider.ErrNotFound)
	out, err, _, kept := runDownWith(t, "proj-a", nf)
	if err != nil {
		t.Fatalf("down: %v", err)
	}
	if kept || strings.Contains(out, "Terminated.") || !strings.Contains(out, "not found in project proj-a") {
		t.Fatalf("want entry removed + not-found message; kept=%v out=%q", kept, out)
	}
}

// sync maps a 404 to terminated. Without a recorded project that may be the
// wrong project, so the entry must stay; with one, it is removed.
func TestSync_notFoundRemovesOnlyWithRecordedProject(t *testing.T) {
	for _, tc := range []struct {
		project  string
		wantKept bool
	}{{"", true}, {"proj-a", false}} {
		t.Setenv("RUNLEASE_HOME", t.TempDir())
		reg, _ := loadRegistry()
		_ = reg.Add(&registry.Entry{Handle: provider.InstanceHandle{
			ID: "rl-box-abc123", ManifestName: "box", Provider: provider.ProviderGCP,
			Region: "europe-west1-b", Project: tc.project, State: provider.StatusRunning,
		}, CredDir: t.TempDir()})

		orig := newProvider
		newProvider = func(context.Context, provider.ProviderType, string, string) (provider.CloudProvider, error) {
			return &fakeDownProvider{status: provider.StatusTerminated}, nil
		}
		var buf bytes.Buffer
		syncCmd.SetOut(&buf)
		syncCmd.SetErr(&buf)
		syncCmd.SetContext(context.Background())
		err := runSync(syncCmd, nil)
		newProvider = orig
		if err != nil {
			t.Fatalf("sync: %v", err)
		}
		reg2, _ := loadRegistry()
		_, gerr := reg2.Get("box")
		if kept := gerr == nil; kept != tc.wantKept {
			t.Errorf("project=%q: kept=%v, want %v; out=%q", tc.project, kept, tc.wantKept, buf.String())
		}
	}
}
