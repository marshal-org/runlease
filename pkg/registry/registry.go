package registry

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/runlease/runlease/pkg/provider"
)

type Entry struct {
	Handle     provider.InstanceHandle `json:"handle"`
	CredDir    string                  `json:"credDir"`
	RelayAddr  string                  `json:"relayAddr,omitempty"`
	RelayToken string                  `json:"relayToken,omitempty"`
}

type Registry struct {
	path string
	mu   sync.Mutex
	data map[string]*Entry // keyed by manifest name
}

func Load(path string) (*Registry, error) {
	r := &Registry{path: path, data: make(map[string]*Entry)}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return r, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading registry: %w", err)
	}
	if err := json.Unmarshal(raw, &r.data); err != nil {
		return nil, fmt.Errorf("parsing registry: %w", err)
	}
	return r, nil
}

func (r *Registry) Add(e *Entry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data[e.Handle.ManifestName] = e
	return r.save()
}

func (r *Registry) Get(nameOrID string) (*Entry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := r.findKey(nameOrID)
	if key == "" {
		return nil, fmt.Errorf("instance %q not found", nameOrID)
	}
	return r.data[key], nil
}

func (r *Registry) UpdateState(nameOrID string, state provider.InstanceStatus) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := r.findKey(nameOrID)
	if key == "" {
		return fmt.Errorf("instance %q not found", nameOrID)
	}
	r.data[key].Handle.State = state
	return r.save()
}

func (r *Registry) Remove(nameOrID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := r.findKey(nameOrID)
	if key == "" {
		return fmt.Errorf("instance %q not found", nameOrID)
	}
	delete(r.data, key)
	return r.save()
}

func (r *Registry) List() []*Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*Entry, 0, len(r.data))
	for _, e := range r.data {
		out = append(out, e)
	}
	return out
}

// findKey must be called with mu held.
func (r *Registry) findKey(nameOrID string) string {
	if _, ok := r.data[nameOrID]; ok {
		return nameOrID
	}
	for k, e := range r.data {
		if string(e.Handle.ID) == nameOrID {
			return k
		}
	}
	return ""
}

func (r *Registry) save() error {
	if err := os.MkdirAll(filepath.Dir(r.path), 0700); err != nil {
		return fmt.Errorf("creating registry dir: %w", err)
	}
	raw, err := json.MarshalIndent(r.data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling registry: %w", err)
	}
	if err := os.WriteFile(r.path, raw, 0600); err != nil {
		return fmt.Errorf("writing registry: %w", err)
	}
	return nil
}
