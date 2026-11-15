package lifecycle

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/marshal-org/runlease/pkg/credential"
	"github.com/marshal-org/runlease/pkg/provider"
	"github.com/marshal-org/runlease/pkg/registry"
)

// ProviderFactory matches the signature of newProvider in cmd/runlease/root.go,
// allowing the manager to be tested without real cloud credentials.
type ProviderFactory func(ctx context.Context, pt provider.ProviderType, region, project string) (provider.CloudProvider, error)

type Manager struct {
	reg          *registry.Registry
	factory      ProviderFactory
	log          *slog.Logger
	pollInterval time.Duration // how often to check for spot interruption / unexpected termination

	// OnCleanup, if set, runs just before an entry is dropped from the
	// registry. The entry holds the relay token, so this is the last chance to
	// give the relay slot back. It must not block for long.
	OnCleanup func(e *registry.Entry)

	mu      sync.Mutex
	watched map[provider.InstanceID]struct{} // instances that have a watch goroutine
}

func New(reg *registry.Registry, factory ProviderFactory, log *slog.Logger) *Manager {
	return &Manager{
		reg:          reg,
		factory:      factory,
		log:          log,
		pollInterval: 30 * time.Second,
	}
}

// Start watches every registry entry until ctx is cancelled. It scans the
// registry again at every poll interval, so machines launched after the daemon
// started are picked up too.
func (m *Manager) Start(ctx context.Context) {
	var wg sync.WaitGroup
	m.Reconcile(ctx, &wg)
	m.log.Info("lifecycle manager ready")
	ticker := time.NewTicker(m.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			m.Reconcile(ctx, &wg)
		case <-ctx.Done():
			m.log.Info("lifecycle manager stopping, draining goroutines")
			wg.Wait()
			return
		}
	}
}

// Reconcile starts a watch for every registry entry that does not have one.
// Each watch then holds that instance to its lease:
//   - gone from the cloud        → clean up local state
//   - powered off but not deleted → delete it, then clean up
//   - past its TTL               → terminate, and keep trying until it works
//   - no TTL set                 → left alone, but still cleaned up once gone
//
// It never gives up on an entry because of an error: a provider that cannot be
// built or a status call that fails is tried again at the next poll.
func (m *Manager) Reconcile(ctx context.Context, wg *sync.WaitGroup) {
	if err := m.reg.Reload(); err != nil {
		m.log.Error("reloading registry", "err", err)
	}
	for _, e := range m.reg.List() {
		if m.claim(e.Handle.ID) {
			m.log.Info("watching instance", "id", e.Handle.ID, "name", e.Handle.ManifestName)
			m.watch(ctx, wg, *e)
		}
	}
}

// claim marks an instance as watched and reports whether the caller is the
// one that should start its watch.
func (m *Manager) claim(id provider.InstanceID) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.watched == nil {
		m.watched = make(map[provider.InstanceID]struct{})
	}
	if _, ok := m.watched[id]; ok {
		return false
	}
	m.watched[id] = struct{}{}
	return true
}

func (m *Manager) release(id provider.InstanceID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.watched, id)
}

// watch runs one instance's lease. e is the watch's own copy of the entry.
func (m *Manager) watch(ctx context.Context, wg *sync.WaitGroup, e registry.Entry) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer m.release(e.Handle.ID)

		// The timer only wakes the loop at the deadline. The decision to
		// terminate is made in step, from the registry as it is at that moment.
		ttlTimer := time.NewTimer(time.Hour)
		stopTimer(ttlTimer)
		defer ttlTimer.Stop()
		arm := func() {
			stopTimer(ttlTimer)
			if e.Handle.ExpiresAt != nil {
				ttlTimer.Reset(max(time.Until(*e.Handle.ExpiresAt), 0))
			}
		}
		pollTicker := time.NewTicker(m.pollInterval)
		defer pollTicker.Stop()

		var p provider.CloudProvider
		arm()
		for {
			if m.step(ctx, &p, &e, arm) {
				return
			}
			select {
			case <-ttlTimer.C:
			case <-pollTicker.C:
			case <-ctx.Done():
				return
			}
		}
	}()
}

// step checks one instance once and reports whether its watch is finished.
func (m *Manager) step(ctx context.Context, p *provider.CloudProvider, e *registry.Entry, arm func()) (done bool) {
	// Read the lease from disk first, so a `runlease snooze` written a moment
	// before the old deadline is honoured rather than raced.
	if err := m.reg.Reload(); err == nil {
		fresh, err := m.reg.Get(string(e.Handle.ID))
		if err != nil {
			// Removed by `down` or `sync` in another process.
			return true
		}
		if !sameTime(fresh.Handle.ExpiresAt, e.Handle.ExpiresAt) {
			e.Handle.ExpiresAt = fresh.Handle.ExpiresAt
			arm()
			m.log.Info("lease changed", "id", e.Handle.ID, "name", e.Handle.ManifestName, "expires_at", e.Handle.ExpiresAt)
		}
	}

	if *p == nil {
		np, err := m.factory(ctx, e.Handle.Provider, e.Handle.Region, e.Handle.Project)
		if err != nil {
			m.log.Error("creating provider, will retry", "provider", e.Handle.Provider, "err", err)
			return false
		}
		*p = np
	}
	status, err := (*p).Status(ctx, e.Handle.ID)
	if err != nil {
		m.log.Error("checking instance status, will retry", "id", e.Handle.ID, "name", e.Handle.ManifestName, "err", err)
		return false
	}

	switch status {
	case provider.StatusTerminated:
		if e.Handle.ProjectUnknown() {
			// Status maps a 404 to terminated; without a recorded project
			// that may just be the wrong project.
			m.log.Warn("instance not found in the default project and no project recorded, keeping registry entry",
				"id", e.Handle.ID, "name", e.Handle.ManifestName)
			return false
		}
		m.log.Info("instance is gone, cleaning up", "id", e.Handle.ID, "name", e.Handle.ManifestName)
		m.cleanup(e)
		return true
	case provider.StatusStopped:
		// Powered off is not deleted: the machine and its disk still exist and
		// are still billed. A runlease machine is never meant to be restarted.
		m.log.Warn("instance is stopped but not deleted, deleting it", "id", e.Handle.ID, "name", e.Handle.ManifestName)
		return m.terminate(ctx, *p, e)
	}
	if e.Handle.ExpiresAt != nil && !time.Now().Before(*e.Handle.ExpiresAt) {
		m.log.Info("TTL expired, reclaiming", "id", e.Handle.ID, "name", e.Handle.ManifestName)
		return m.terminate(ctx, *p, e)
	}
	return false
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

// stopTimer stops t, draining its channel if it had already fired, so a later
// Reset starts clean.
func stopTimer(t *time.Timer) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
}

// terminate deletes the instance and reports whether it is gone. On failure
// the entry is kept and the watch goes on, so the next poll tries again.
func (m *Manager) terminate(ctx context.Context, p provider.CloudProvider, e *registry.Entry) bool {
	if err := p.Terminate(ctx, e.Handle.ID); err != nil &&
		(!errors.Is(err, provider.ErrNotFound) || e.Handle.ProjectUnknown()) {
		// Keep the entry: dropping it would lose track of a VM that may still
		// run (a not-found without a recorded project may be the wrong project).
		m.log.Error("terminating instance failed, keeping registry entry and retrying", "id", e.Handle.ID, "err", err)
		return false
	}
	m.cleanup(e)
	return true
}

func (m *Manager) cleanup(e *registry.Entry) {
	if err := credential.Delete(e.CredDir, string(e.Handle.ID)); err != nil {
		m.log.Error("deleting credential", "id", e.Handle.ID, "err", err)
	}
	if m.OnCleanup != nil {
		m.OnCleanup(e)
	}
	// By ID, not by name: the name may by now belong to a newer instance.
	if err := m.reg.RemoveID(e.Handle.ID); err != nil {
		m.log.Error("removing from registry", "name", e.Handle.ManifestName, "err", err)
	}
}
