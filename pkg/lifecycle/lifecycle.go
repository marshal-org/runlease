package lifecycle

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/runlease/runlease/pkg/credential"
	"github.com/runlease/runlease/pkg/provider"
	"github.com/runlease/runlease/pkg/registry"
)

// ProviderFactory matches the signature of newProvider in cmd/runlease/root.go,
// allowing the manager to be tested without real cloud credentials.
type ProviderFactory func(ctx context.Context, pt provider.ProviderType, region string) (provider.CloudProvider, error)

type Manager struct {
	reg          *registry.Registry
	factory      ProviderFactory
	log          *slog.Logger
	pollInterval time.Duration // how often to check for spot interruption / unexpected termination
}

func New(reg *registry.Registry, factory ProviderFactory, log *slog.Logger) *Manager {
	return &Manager{
		reg:          reg,
		factory:      factory,
		log:          log,
		pollInterval: 30 * time.Second,
	}
}

// Start reconciles all registry entries, then blocks until ctx is cancelled.
func (m *Manager) Start(ctx context.Context) {
	var wg sync.WaitGroup
	m.Reconcile(ctx, &wg)
	m.log.Info("lifecycle manager ready")
	<-ctx.Done()
	m.log.Info("lifecycle manager stopping, draining goroutines")
	wg.Wait()
}

// Reconcile inspects every registry entry against its actual cloud state:
//   - already terminated/stopped → clean up local state
//   - running but TTL expired   → terminate immediately
//   - running within TTL        → arm a watch goroutine
//   - no TTL set                → skip (manually managed)
func (m *Manager) Reconcile(ctx context.Context, wg *sync.WaitGroup) {
	entries := m.reg.List()
	m.log.Info("reconciling", "entries", len(entries))

	for _, e := range entries {
		e := e
		p, err := m.factory(ctx, e.Handle.Provider, e.Handle.Region)
		if err != nil {
			m.log.Error("creating provider", "provider", e.Handle.Provider, "err", err)
			continue
		}
		status, err := p.Status(ctx, e.Handle.ID)
		if err != nil {
			m.log.Error("checking instance status", "id", e.Handle.ID, "name", e.Handle.ManifestName, "err", err)
			continue
		}
		switch status {
		case provider.StatusTerminated, provider.StatusStopped:
			m.log.Info("cleaning up terminated instance", "id", e.Handle.ID, "name", e.Handle.ManifestName)
			m.cleanup(e)
		case provider.StatusRunning, provider.StatusPending:
			if e.Handle.ExpiresAt == nil {
				m.log.Info("skipping manually-managed instance", "id", e.Handle.ID, "name", e.Handle.ManifestName)
				continue
			}
			remaining := time.Until(*e.Handle.ExpiresAt)
			if remaining <= 0 {
				m.log.Info("TTL already expired, terminating", "id", e.Handle.ID, "name", e.Handle.ManifestName)
				m.terminate(ctx, p, e)
			} else {
				m.log.Info("watching instance", "id", e.Handle.ID, "name", e.Handle.ManifestName, "expires_in", remaining.Round(time.Second))
				m.watch(ctx, wg, p, e)
			}
		default:
			m.log.Warn("instance in unexpected state", "id", e.Handle.ID, "state", status)
		}
	}
}

func (m *Manager) watch(ctx context.Context, wg *sync.WaitGroup, p provider.CloudProvider, e *registry.Entry) {
	remaining := time.Until(*e.Handle.ExpiresAt)
	wg.Add(1)
	go func() {
		defer wg.Done()
		ttlTimer := time.NewTimer(remaining)
		defer ttlTimer.Stop()
		pollTicker := time.NewTicker(m.pollInterval)
		defer pollTicker.Stop()

		for {
			select {
			case <-ttlTimer.C:
				m.log.Info("TTL expired, reclaiming", "id", e.Handle.ID, "name", e.Handle.ManifestName)
				m.terminate(ctx, p, e)
				return
			case <-pollTicker.C:
				// Pick up out-of-band TTL changes (e.g. `runlease snooze`
				// pushing back the deadline from another process) and re-arm.
				if err := m.reg.Reload(); err == nil {
					if fresh, ferr := m.reg.Get(string(e.Handle.ID)); ferr == nil && fresh.Handle.ExpiresAt != nil {
						if !fresh.Handle.ExpiresAt.Equal(*e.Handle.ExpiresAt) {
							e.Handle.ExpiresAt = fresh.Handle.ExpiresAt
							if !ttlTimer.Stop() {
								select {
								case <-ttlTimer.C:
								default:
								}
							}
							ttlTimer.Reset(time.Until(*fresh.Handle.ExpiresAt))
							m.log.Info("TTL updated, re-armed", "id", e.Handle.ID, "name", e.Handle.ManifestName,
								"expires_in", time.Until(*fresh.Handle.ExpiresAt).Round(time.Second))
						}
					}
				}
				status, err := p.Status(ctx, e.Handle.ID)
				if err != nil {
					m.log.Error("polling instance status", "id", e.Handle.ID, "err", err)
					continue
				}
				if status == provider.StatusTerminated || status == provider.StatusStopped {
					m.log.Warn("instance no longer running (spot interruption or external action), cleaning up",
						"id", e.Handle.ID, "name", e.Handle.ManifestName, "status", status)
					// terminate is idempotent on AWS; error is logged and ignored.
					m.terminate(ctx, p, e)
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (m *Manager) terminate(ctx context.Context, p provider.CloudProvider, e *registry.Entry) {
	if err := p.Terminate(ctx, e.Handle.ID); err != nil {
		m.log.Error("terminating instance", "id", e.Handle.ID, "err", err)
	}
	m.cleanup(e)
}

func (m *Manager) cleanup(e *registry.Entry) {
	if err := credential.Delete(e.CredDir, string(e.Handle.ID)); err != nil {
		m.log.Error("deleting credential", "id", e.Handle.ID, "err", err)
	}
	if err := m.reg.Remove(e.Handle.ManifestName); err != nil {
		m.log.Error("removing from registry", "name", e.Handle.ManifestName, "err", err)
	}
}
