package instanceclass

import (
	_ "embed"
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/marshal-org/runlease/pkg/provider"
)

//go:embed classes.yaml
var defaultCatalogBytes []byte

type catalog struct {
	Classes map[string]map[string][]string `yaml:"classes"`
}

type Resolver struct {
	cat catalog
}

func LoadDefault() (*Resolver, error) {
	return load(defaultCatalogBytes)
}

func LoadFile(path string) (*Resolver, error) {
	// Reserved for future custom catalogs; for now callers use LoadDefault.
	_ = path
	return nil, fmt.Errorf("custom catalog files not yet supported — use LoadDefault")
}

func load(data []byte) (*Resolver, error) {
	var c catalog
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parsing instance class catalog: %w", err)
	}
	return &Resolver{cat: c}, nil
}

// Resolve returns the ordered list of concrete instance types for the given
// abstract class and provider. The first element is the preferred type.
func (r *Resolver) Resolve(class string, pt provider.ProviderType) ([]string, error) {
	byProvider, ok := r.cat.Classes[class]
	if !ok {
		return nil, fmt.Errorf("unknown instance class %q", class)
	}
	candidates, ok := byProvider[string(pt)]
	if !ok || len(candidates) == 0 {
		return nil, fmt.Errorf("no instance types defined for class %q on provider %q", class, pt)
	}
	return candidates, nil
}
