package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/marshal-org/runlease/pkg/provider"
	"github.com/marshal-org/runlease/pkg/registry"
)

// mockProvider returns statuses from a sequence, repeating the last entry.
type mockProvider struct {
	statuses     []provider.InstanceStatus
	statusIdx    int
	terminateCh  chan struct{}
	terminateErr error
}

func newMock(status provider.InstanceStatus) *mockProvider {
	return &mockProvider{statuses: []provider.InstanceStatus{status}, terminateCh: make(chan struct{})}
}

func newMockSeq(statuses ...provider.InstanceStatus) *mockProvider {
	return &mockProvider{statuses: statuses, terminateCh: make(chan struct{})}
}

func (m *mockProvider) RequestSpot(_ context.Context, _ provider.InstanceSpec) (*provider.InstanceHandle, error) {
	return nil, errors.New("not implemented")
}
func (m *mockProvider) Terminate(_ context.Context, _ provider.InstanceID) error {
	select {
	case <-m.terminateCh:
	default:
		close(m.terminateCh)
	}
	return m.terminateErr
}
func (m *mockProvider) Status(_ context.Context, _ provider.InstanceID) (provider.InstanceStatus, error) {
	idx := m.statusIdx
	if idx >= len(m.statuses) {
		idx = len(m.statuses) - 1
	}
	m.statusIdx++
	return m.statuses[idx], nil
}
func (m *mockProvider) WaitUntilRunning(_ context.Context, _ provider.InstanceID) (*provider.InstanceHandle, error) {
	return nil, errors.New("not implemented")
}

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func setupRegistry(t *testing.T, entries ...*registry.Entry) *registry.Registry {
	t.Helper()
	reg, err := registry.Load(filepath.Join(t.TempDir(), "registry.json"))
	if err != nil {
		t.Fatalf("registry.Load: %v", err)
	}
	for _, e := range entries {
		if err := reg.Add(e); err != nil {
			t.Fatalf("reg.Add: %v", err)
		}
	}
	return reg
}

func entry(t *testing.T, name, id string, expiresAt *time.Time) *registry.Entry {
	t.Helper()
	return &registry.Entry{
		Handle: provider.InstanceHandle{
			ID:           provider.InstanceID(id),
			ManifestName: name,
			Provider:     provider.ProviderAWS,
			Region:       "us-east-1",
			State:        provider.StatusRunning,
			CreatedAt:    time.Now().UTC(),
			ExpiresAt:    expiresAt,
		},
		CredDir: t.TempDir(),
	}
}

func futureExpiry(d time.Duration) *time.Time { t := time.Now().Add(d); return &t }
func pastExpiry() *time.Time                  { t := time.Now().Add(-time.Minute); return &t }

func managerWith(reg *registry.Registry, mock *mockProvider) *Manager {
	factory := func(_ context.Context, _ provider.ProviderType, _, _ string) (provider.CloudProvider, error) {
		return mock, nil
	}
	return &Manager{reg: reg, factory: factory, log: silentLogger(), pollInterval: time.Millisecond}
}

// reconcile runs the manager for a short while (many poll intervals) and
// waits for its watches to end. A watch only ends by itself once its instance
// is gone, so the deadline is what stops the rest.
func reconcile(mgr *Manager) {
	var wg sync.WaitGroup
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	mgr.Reconcile(ctx, &wg)
	wg.Wait()
}

func TestReconcile_cleansUpTerminatedInstance(t *testing.T) {
	e := entry(t, "dead-runner", "i-dead", nil)
	reg := setupRegistry(t, e)
	reconcile(managerWith(reg, newMock(provider.StatusTerminated)))
	if _, err := reg.Get("dead-runner"); err == nil {
		t.Error("terminated instance should have been removed from registry")
	}
}

func TestReconcile_terminatesExpiredInstance(t *testing.T) {
	e := entry(t, "expired-runner", "i-exp", pastExpiry())
	reg := setupRegistry(t, e)
	mock := newMock(provider.StatusRunning)
	reconcile(managerWith(reg, mock))
	select {
	case <-mock.terminateCh:
	default:
		t.Error("Terminate not called for expired instance")
	}
	if _, err := reg.Get("expired-runner"); err == nil {
		t.Error("expired instance should have been removed from registry")
	}
}

func TestReconcile_armsTimerForLiveInstance(t *testing.T) {
	e := entry(t, "live-runner", "i-live", futureExpiry(80*time.Millisecond))
	reg := setupRegistry(t, e)
	mock := newMock(provider.StatusRunning)

	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := managerWith(reg, mock)
	mgr.Reconcile(ctx, &wg)

	select {
	case <-mock.terminateCh:
		t.Error("Terminate fired before TTL elapsed")
	default:
	}
	select {
	case <-mock.terminateCh:
	case <-time.After(500 * time.Millisecond):
		t.Error("Terminate not called after TTL elapsed")
	}
	wg.Wait()
}

func TestReconcile_skipsInstanceWithNoTTL(t *testing.T) {
	e := entry(t, "manual-runner", "i-manual", nil)
	reg := setupRegistry(t, e)
	mock := newMock(provider.StatusRunning)
	reconcile(managerWith(reg, mock))
	select {
	case <-mock.terminateCh:
		t.Error("Terminate should not be called for manually-managed instance")
	default:
	}
	if _, err := reg.Get("manual-runner"); err != nil {
		t.Error("manually-managed instance should remain in registry")
	}
}

func TestReconcile_watchGoroutineExitsOnContextCancel(t *testing.T) {
	e := entry(t, "long-runner", "i-long", futureExpiry(time.Hour))
	reg := setupRegistry(t, e)
	mock := newMock(provider.StatusRunning)

	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())
	mgr := managerWith(reg, mock)
	mgr.Reconcile(ctx, &wg)

	cancel()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Error("watch goroutine did not exit after context cancel")
	}
	select {
	case <-mock.terminateCh:
		t.Error("Terminate should not be called on context cancel")
	default:
	}
}

func TestWatch_cleansUpOnSpotInterruption(t *testing.T) {
	// Instance starts running, then transitions to terminated (spot interruption).
	e := entry(t, "spot-runner", "i-spot", futureExpiry(time.Hour))
	reg := setupRegistry(t, e)
	// First poll returns running; second returns terminated (simulates interruption notice).
	mock := newMockSeq(provider.StatusRunning, provider.StatusTerminated)

	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := managerWith(reg, mock)
	mgr.Reconcile(ctx, &wg)

	// Goroutine polls every millisecond; a gone instance ends the watch.
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("watch did not end after the instance was gone")
	}
	if _, err := reg.Get("spot-runner"); err == nil {
		t.Error("spot-interrupted instance should have been removed from registry")
	}
}

func TestReconcile_failedTerminateKeepsEntry(t *testing.T) {
	e := entry(t, "stuck-runner", "i-stuck", pastExpiry())
	reg := setupRegistry(t, e)
	mock := newMock(provider.StatusRunning)
	mock.terminateErr = errors.New("instance still exists")
	reconcile(managerWith(reg, mock))
	if _, err := reg.Get("stuck-runner"); err != nil {
		t.Error("entry must be kept when Terminate fails — the VM may still be running")
	}
}

func gcpEntry(t *testing.T, name, project string, expiresAt *time.Time) *registry.Entry {
	e := entry(t, name, "rl-"+name, expiresAt)
	e.Handle.Provider = provider.ProviderGCP
	e.Handle.Region = "europe-west1-b"
	e.Handle.Project = project
	return e
}

var errNotFound = fmt.Errorf("instance not in project: %w", provider.ErrNotFound)

// No recorded project: a 404 may be the wrong project, so the entry stays.
func TestReconcile_legacyGCPNotFoundKeepsEntry(t *testing.T) {
	reg := setupRegistry(t, gcpEntry(t, "legacy", "", nil))
	reconcile(managerWith(reg, newMock(provider.StatusTerminated)))
	if _, err := reg.Get("legacy"); err != nil {
		t.Error("legacy GCP entry must be kept when Status reads terminated (404)")
	}
}

func TestReconcile_legacyGCPTerminateNotFoundKeepsEntry(t *testing.T) {
	reg := setupRegistry(t, gcpEntry(t, "legacy", "", pastExpiry()))
	mock := newMock(provider.StatusRunning)
	mock.terminateErr = errNotFound
	reconcile(managerWith(reg, mock))
	if _, err := reg.Get("legacy"); err != nil {
		t.Error("legacy GCP entry must be kept when Terminate says not found")
	}
}

func TestReconcile_recordedProjectTerminateNotFoundRemovesEntry(t *testing.T) {
	reg := setupRegistry(t, gcpEntry(t, "modern", "proj-a", pastExpiry()))
	mock := newMock(provider.StatusRunning)
	mock.terminateErr = errNotFound
	reconcile(managerWith(reg, mock))
	if _, err := reg.Get("modern"); err == nil {
		t.Error("not found in the recorded project means gone: entry should be removed")
	}
}

// The entry carries the relay token, so the hook has to run while the entry is
// still in the registry: afterwards nothing could release the slot.
func TestCleanup_callsOnCleanupBeforeRemoving(t *testing.T) {
	reg := setupRegistry(t, entry(t, "worker", "i-dead", nil))
	mgr := managerWith(reg, newMock(provider.StatusTerminated))

	var seen string
	stillRegistered := false
	mgr.OnCleanup = func(e *registry.Entry) {
		seen = e.Handle.ManifestName
		_, err := reg.Get("worker")
		stillRegistered = err == nil
	}
	reconcile(mgr)

	if seen != "worker" {
		t.Fatalf("OnCleanup saw %q, want worker", seen)
	}
	if !stillRegistered {
		t.Error("OnCleanup ran after the entry was removed")
	}
	if _, err := reg.Get("worker"); err == nil {
		t.Error("entry was not removed after cleanup")
	}
}

// A machine launched after the daemon started must still be held to its lease.
func TestStart_picksUpInstancesAddedLater(t *testing.T) {
	reg := setupRegistry(t)
	mock := newMock(provider.StatusRunning)
	mgr := managerWith(reg, mock)
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { mgr.Start(ctx); close(stopped) }()
	// Stop the manager and wait for its watches before the temp dir is removed:
	// they write to the registry file in it.
	defer func() { cancel(); <-stopped }()

	time.Sleep(10 * time.Millisecond)
	// Written by another process: a registry of its own on the same file.
	if err := reg.Add(entry(t, "late", "i-late", pastExpiry())); err != nil {
		t.Fatal(err)
	}
	select {
	case <-mock.terminateCh:
	case <-time.After(500 * time.Millisecond):
		t.Error("instance added after start was never terminated at its TTL")
	}
}

// A terminate that fails at TTL is tried again; the entry goes once it works.
func TestWatch_retriesFailedTerminate(t *testing.T) {
	reg := setupRegistry(t, entry(t, "stuck", "i-stuck", pastExpiry()))
	mock := &failThenOK{mockProvider: newMock(provider.StatusRunning), failures: 3}
	factory := func(context.Context, provider.ProviderType, string, string) (provider.CloudProvider, error) {
		return mock, nil
	}
	mgr := &Manager{reg: reg, factory: factory, log: silentLogger(), pollInterval: time.Millisecond}
	var wg sync.WaitGroup
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	mgr.Reconcile(ctx, &wg)
	wg.Wait()
	if mock.calls < 4 {
		t.Errorf("Terminate called %d times, want a retry after each failure", mock.calls)
	}
	if _, err := reg.Get("stuck"); err == nil {
		t.Error("entry should be removed once Terminate succeeds")
	}
}

type failThenOK struct {
	*mockProvider
	failures, calls int
}

func (f *failThenOK) Terminate(context.Context, provider.InstanceID) error {
	f.calls++
	if f.calls <= f.failures {
		return errors.New("still there")
	}
	return nil
}

// A stopped machine still exists and is still billed: delete it, do not just forget it.
func TestWatch_deletesStoppedInstance(t *testing.T) {
	reg := setupRegistry(t, entry(t, "off", "i-off", nil))
	mock := newMock(provider.StatusStopped)
	reconcile(managerWith(reg, mock))
	select {
	case <-mock.terminateCh:
	default:
		t.Error("stopped instance was forgotten without being deleted")
	}
	if _, err := reg.Get("off"); err == nil {
		t.Error("entry should be removed after the stopped instance is deleted")
	}
}

// Cleanup of an old instance must not remove a newer one that reuses its name.
func TestCleanup_leavesNewerInstanceWithSameName(t *testing.T) {
	old := entry(t, "dev", "i-old", nil)
	reg := setupRegistry(t, old)
	mgr := managerWith(reg, newMock(provider.StatusTerminated))
	if err := reg.Add(entry(t, "dev", "i-new", nil)); err != nil {
		t.Fatal(err)
	}
	mgr.cleanup(old)
	got, err := reg.Get("dev")
	if err != nil || got.Handle.ID != "i-new" {
		t.Errorf("the newer instance's entry was removed: %v", err)
	}
}

// A snooze written just before the old deadline wins over the timer.
func TestWatch_snoozeJustBeforeDeadlineIsHonoured(t *testing.T) {
	reg := setupRegistry(t, entry(t, "napper", "i-nap", futureExpiry(30*time.Millisecond)))
	mock := newMock(provider.StatusRunning)
	mgr := managerWith(reg, mock)
	mgr.pollInterval = time.Hour // only the deadline timer can wake the watch
	var wg sync.WaitGroup
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	mgr.Reconcile(ctx, &wg)
	if err := reg.UpdateExpiry("napper", futureExpiry(time.Hour)); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	select {
	case <-mock.terminateCh:
		t.Error("instance terminated at the old deadline although it was snoozed")
	default:
	}
}
