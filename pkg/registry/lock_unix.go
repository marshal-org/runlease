//go:build !windows

package registry

import (
	"os"
	"syscall"
)

// lockFile takes an exclusive lock that other runlease processes honour, and
// returns the function that gives it up. The lock is on a file of its own
// because the registry file itself is replaced by a rename on every write.
func lockFile(path string) (unlock func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
