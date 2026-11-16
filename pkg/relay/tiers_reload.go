package relay

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"time"
)

// Limits are checked when a machine registers and at no other time, so they
// can be replaced while the relay runs: sessions already connected keep the
// terms they registered with, and the next registration sees the new file.

// tiers returns the limits in force now.
func (s *Server) tiers() *Tiers { return s.live.Load() }

// SetTiers replaces the limits. Nil removes every per-identity limit.
func (s *Server) SetTiers(t *Tiers) { s.live.Store(t) }

// ReloadTiers reads the tiers file and, when its content has changed since
// the last read, puts the new limits in force. A file that cannot be read or
// is not valid changes nothing: the relay keeps the limits it has, and the
// error is returned for the caller to log. It reports whether the limits
// were replaced.
func (s *Server) ReloadTiers(path string) (bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("reading tiers file: %w", err)
	}
	sum := sha256.Sum256(raw)

	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	if bytes.Equal(sum[:], s.tiersSum) {
		return false, nil
	}
	// Remember this content whether or not it is valid, so a bad file is
	// reported once and not on every poll.
	s.tiersSum = sum[:]
	t, err := ParseTiers(raw)
	if err != nil {
		return false, err
	}
	s.SetTiers(t)
	return true, nil
}

// WatchTiers keeps the limits in step with the tiers file until ctx ends,
// reading it every interval. The first read only records what the file holds:
// the relay loaded it moments ago. Reloads and refusals are logged.
func (s *Server) WatchTiers(ctx context.Context, path string, interval time.Duration) {
	if raw, err := os.ReadFile(path); err == nil {
		sum := sha256.Sum256(raw)
		s.reloadMu.Lock()
		if s.tiersSum == nil {
			s.tiersSum = sum[:]
		}
		s.reloadMu.Unlock()
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.reloadAndLog(path)
		}
	}
}

// ReloadTiersNow is ReloadTiers with the outcome logged, for a signal handler.
func (s *Server) ReloadTiersNow(path string) { s.reloadAndLog(path) }

func (s *Server) reloadAndLog(path string) {
	changed, err := s.ReloadTiers(path)
	switch {
	case err != nil:
		s.log.Error("tiers file not applied, keeping the limits in force", "path", path, "err", err)
	case changed:
		t := s.tiers()
		s.log.Info("tiers reloaded", "default", t.Default, "tiers", len(t.Tiers),
			"assigned_emails", len(t.Assign.Emails), "assigned_domains", len(t.Assign.Domains))
	}
}
