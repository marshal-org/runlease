package manifest

import (
	"html"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every manifest under examples/manifests must load and validate, so an
// example cannot drift from what the tool accepts.
func TestExampleManifestsLoad(t *testing.T) {
	files, err := filepath.Glob("../../examples/manifests/*.yaml")
	if err != nil || len(files) == 0 {
		t.Fatalf("no example manifests found: %v", err)
	}
	for _, f := range files {
		if _, err := Load(f); err != nil {
			t.Errorf("%s: %v", filepath.Base(f), err)
		}
	}
}

// The install page shows these examples. What it shows must be the file,
// byte for byte, so a reader can copy it and it works.
func TestInstallPageShowsExamplesVerbatim(t *testing.T) {
	page, err := os.ReadFile("../../site/install.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"quick-gcp", "quick-aws", "agent-workbench", "no-nat", "gpu"} {
		raw, err := os.ReadFile("../../examples/manifests/" + name + ".yaml")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(page), html.EscapeString(strings.TrimRight(string(raw), "\n"))) {
			t.Errorf("site/install.html does not contain examples/manifests/%s.yaml verbatim", name)
		}
	}
}
