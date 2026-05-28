package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverProjectManifest(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(deep, 0755); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(root, "a", "runlease.yaml")
	if err := os.WriteFile(manifest, []byte("name: x\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// chdir into the deepest directory and verify discovery walks up to the manifest.
	prevCwd, _ := os.Getwd()
	defer os.Chdir(prevCwd) //nolint:errcheck
	if err := os.Chdir(deep); err != nil {
		t.Fatal(err)
	}
	got, err := discoverProjectManifest()
	if err != nil {
		t.Fatalf("discoverProjectManifest: %v", err)
	}
	// Compare via Stat to handle macOS /var ↔ /private/var symlink resolution.
	wantInfo, _ := os.Stat(manifest)
	gotInfo, _ := os.Stat(got)
	if !os.SameFile(wantInfo, gotInfo) {
		t.Errorf("discovered %q, want %q (same file)", got, manifest)
	}
}

func TestDiscoverProjectManifest_returnsEmptyWhenAbsent(t *testing.T) {
	root := t.TempDir()
	prevCwd, _ := os.Getwd()
	defer os.Chdir(prevCwd) //nolint:errcheck
	// Set HOME above root so the walk stops cleanly at the temp dir boundary.
	t.Setenv("HOME", root)
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	got, err := discoverProjectManifest()
	if err != nil {
		t.Fatalf("discoverProjectManifest: %v", err)
	}
	if got != "" {
		t.Errorf("want empty result, got %q", got)
	}
}
