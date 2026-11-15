package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/marshal-org/runlease/pkg/provider"
)

type Entry struct {
	Handle     provider.InstanceHandle `json:"handle"`
	CredDir    string                  `json:"credDir"`
	RelayAddr  string                  `json:"relayAddr,omitempty"`
	RelayToken string                  `json:"relayToken,omitempty"`
	// Forwards holds the manifest.Forwards list at the time of `up`, so
	// `forward --all` and `up --attach` can recreate them without re-reading
	// the manifest. Stored as opaque JSON to avoid a registry → manifest import.
	Forwards []ForwardSpec `json:"forwards,omitempty"`
}

// ForwardSpec is a registry-side copy of manifest.ForwardSpec, kept here to
// keep registry self-contained (no manifest import).
type ForwardSpec struct {
	Name      string `json:"name,omitempty"`
	Direction string `json:"direction,omitempty"`
	Bind      string `json:"bind,omitempty"`
	Local     int    `json:"local"`
	Remote    int    `json:"remote"`
	Host      string `json:"host,omitempty"`
}

type Registry struct {
	path string
	mu   sync.Mutex
	data map[string]*Entry // keyed by manifest name
}

func Load(path string) (*Registry, error) {
	data, err := readFile(path)
	if err != nil {
		return nil, err
	}
	return &Registry{path: path, data: data}, nil
}

// ErrExists is returned by AddNew when the name is already taken.
var ErrExists = errors.New("an instance with this name is already in the registry")

// Every write goes through mutate: under a file lock it re-reads the registry
// from disk, applies one change, and writes the result back atomically. A
// process that loaded the file a while ago (a `down` waiting on a delete, the
// daemon) therefore changes only its own entry and cannot write back a stale
// copy of everyone else's.
func (r *Registry) mutate(change func(data map[string]*Entry) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(r.path), 0700); err != nil {
		return fmt.Errorf("creating registry dir: %w", err)
	}
	unlock, err := lockFile(r.path + ".lock")
	if err != nil {
		return fmt.Errorf("locking registry: %w", err)
	}
	defer unlock()

	data, err := readFile(r.path)
	if err != nil {
		return err
	}
	if err := change(data); err != nil {
		return err
	}
	if err := writeFile(r.path, data); err != nil {
		return err
	}
	r.data = data
	return nil
}

// Add records an instance, replacing any entry with the same name.
func (r *Registry) Add(e *Entry) error {
	return r.mutate(func(data map[string]*Entry) error {
		data[e.Handle.ManifestName] = e
		return nil
	})
}

// AddNew records an instance and fails with ErrExists if the name is taken.
// The check and the write happen under one lock, so two `up` runs with the
// same name cannot both succeed and leave one machine untracked.
func (r *Registry) AddNew(e *Entry) error {
	return r.mutate(func(data map[string]*Entry) error {
		if _, taken := data[e.Handle.ManifestName]; taken {
			return fmt.Errorf("%q: %w", e.Handle.ManifestName, ErrExists)
		}
		data[e.Handle.ManifestName] = e
		return nil
	})
}

// Update applies change to the entry of the instance with this ID, if it is
// still registered under that ID.
func (r *Registry) Update(id provider.InstanceID, change func(e *Entry)) error {
	return r.mutate(func(data map[string]*Entry) error {
		for _, e := range data {
			if e.Handle.ID == id {
				change(e)
				return nil
			}
		}
		return fmt.Errorf("instance %q not found", id)
	})
}

// RemoveID removes the entry for this exact instance. If the name has since
// been given to another instance, that entry is left alone and RemoveID
// returns nil: the instance asked about is already gone from the registry.
func (r *Registry) RemoveID(id provider.InstanceID) error {
	return r.mutate(func(data map[string]*Entry) error {
		for k, e := range data {
			if e.Handle.ID == id {
				delete(data, k)
			}
		}
		return nil
	})
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
	return r.mutate(func(data map[string]*Entry) error {
		key := findKey(data, nameOrID)
		if key == "" {
			return fmt.Errorf("instance %q not found", nameOrID)
		}
		data[key].Handle.State = state
		return nil
	})
}

// UpdateExpiry sets the instance's auto-termination deadline (used by
// `runlease snooze`). A nil expiresAt clears it (manually managed).
func (r *Registry) UpdateExpiry(nameOrID string, expiresAt *time.Time) error {
	return r.mutate(func(data map[string]*Entry) error {
		key := findKey(data, nameOrID)
		if key == "" {
			return fmt.Errorf("instance %q not found", nameOrID)
		}
		data[key].Handle.ExpiresAt = expiresAt
		return nil
	})
}

// Reload re-reads the registry file from disk, replacing the in-memory state.
// The reclaim daemon calls this so it picks up out-of-band changes (e.g. a
// `runlease snooze` from another process pushing back a TTL).
func (r *Registry) Reload() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	data, err := readFile(r.path)
	if err != nil {
		return err
	}
	r.data = data
	return nil
}

func (r *Registry) Remove(nameOrID string) error {
	return r.mutate(func(data map[string]*Entry) error {
		key := findKey(data, nameOrID)
		if key == "" {
			return fmt.Errorf("instance %q not found", nameOrID)
		}
		delete(data, key)
		return nil
	})
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
func (r *Registry) findKey(nameOrID string) string { return findKey(r.data, nameOrID) }

func findKey(data map[string]*Entry, nameOrID string) string {
	if _, ok := data[nameOrID]; ok {
		return nameOrID
	}
	for k, e := range data {
		if string(e.Handle.ID) == nameOrID {
			return k
		}
	}
	return ""
}

func readFile(path string) (map[string]*Entry, error) {
	data := make(map[string]*Entry)
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return data, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading registry: %w", err)
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("parsing registry: %w", err)
	}
	return data, nil
}

// writeFile replaces the registry through a temp file and a rename, so a crash
// mid-write leaves the old file intact instead of a truncated one.
func writeFile(path string, data map[string]*Entry) error {
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling registry: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("writing registry: %w", err)
	}
	_, err = tmp.Write(raw)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("writing registry: %w", err)
	}
	return nil
}
