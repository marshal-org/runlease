package main

import (
	"errors"
	"os"
	"path/filepath"
)

// discoverProjectManifest walks up from the current working directory looking
// for a runlease.yaml (and also tolerating runlease.yml). The walk stops at
// the filesystem root or at the user's home directory — whichever comes first
// — so a stray runlease.yaml outside the user's projects isn't picked up.
//
// Returns ("", nil) when no manifest is found. Returns a non-nil error only
// for unexpected filesystem failures.
func discoverProjectManifest() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	home, _ := os.UserHomeDir()

	dir := cwd
	for {
		for _, name := range []string{"runlease.yaml", "runlease.yml"} {
			p := filepath.Join(dir, name)
			if info, err := os.Stat(p); err == nil && !info.IsDir() {
				return p, nil
			} else if err != nil && !errors.Is(err, os.ErrNotExist) {
				return "", err
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		if dir == home {
			return "", nil
		}
		dir = parent
	}
}
