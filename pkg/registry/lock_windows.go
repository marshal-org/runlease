//go:build windows

package registry

import (
	"errors"
	"os"
	"time"
)

// lockFile takes an exclusive lock by creating the lock file, which fails
// while another process holds it. A lock older than staleAfter is taken to be
// left over from a process that died, and is broken.
func lockFile(path string) (unlock func(), err error) {
	const (
		staleAfter = 30 * time.Second
		giveUp     = 60 * time.Second
	)
	deadline := time.Now().Add(giveUp)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			f.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if st, serr := os.Stat(path); serr == nil && time.Since(st.ModTime()) > staleAfter {
			_ = os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			return nil, errors.New("timed out waiting for another runlease process")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
