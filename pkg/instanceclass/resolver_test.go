package instanceclass

import (
	"testing"

	"github.com/runlease/runlease/pkg/provider"
)

func resolver(t *testing.T) *Resolver {
	t.Helper()
	r, err := LoadDefault()
	if err != nil {
		t.Fatalf("LoadDefault: %v", err)
	}
	return r
}

func TestResolve_knownClass(t *testing.T) {
	r := resolver(t)
	candidates, err := r.Resolve("compute.large", provider.ProviderAWS)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(candidates) == 0 {
		t.Fatal("expected at least one candidate")
	}
	// First candidate is the preferred type.
	if candidates[0] == "" {
		t.Error("first candidate is empty")
	}
}

func TestResolve_allClassesHaveAWS(t *testing.T) {
	r := resolver(t)
	classes := []string{
		"compute.small", "compute.medium", "compute.large",
		"compute.xlarge", "compute.2xlarge",
	}
	for _, c := range classes {
		t.Run(c, func(t *testing.T) {
			candidates, err := r.Resolve(c, provider.ProviderAWS)
			if err != nil {
				t.Fatalf("Resolve(%q): %v", c, err)
			}
			if len(candidates) == 0 {
				t.Errorf("no candidates for %q", c)
			}
		})
	}
}

func TestResolve_unknownClass(t *testing.T) {
	r := resolver(t)
	_, err := r.Resolve("compute.galaxy", provider.ProviderAWS)
	if err == nil {
		t.Fatal("expected error for unknown class")
	}
}

func TestResolve_unknownProvider(t *testing.T) {
	r := resolver(t)
	_, err := r.Resolve("compute.large", provider.ProviderType("azure"))
	if err == nil {
		t.Fatal("expected error for unsupported provider on this class")
	}
}
