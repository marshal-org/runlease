package lifecycle

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/runlease/runlease/pkg/provider"
	"github.com/runlease/runlease/pkg/registry"
)

// mockProvider returns statuses from a sequence, repeating the last entry.
type mockProvider struct {
	statuses    []provider.InstanceStatus
	statusIdx   int
	terminateCh chan struct{}
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
	return nil
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
	factory := func(_ context.Context, _ provider.ProviderType, _ string) (provider.CloudProvider, error) {
		return mock, nil
	}
	return &Manager{reg: reg, factory: factory, log: silentLogger(), pollInterval: time.Millisecond}
}

func reconcile(mgr *Manager) {
	var wg sync.WaitGroup
	mgr.Reconcile(context.Background(), &wg)
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

	// Goroutine polls every millisecond; terminated state triggers cleanup.
	select {
	case <-mock.terminateCh:
	case <-time.After(500 * time.Millisecond):
		t.Error("cleanup not triggered after spot interruption detected")
	}
	wg.Wait()
	if _, err := reg.Get("spot-runner"); err == nil {
		t.Error("spot-interrupted instance should have been removed from registry")
	}
}
