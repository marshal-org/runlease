package registry

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/marshal-org/runlease/pkg/provider"
)

func makeEntry(name, id string) *Entry {
	return &Entry{
		Handle: provider.InstanceHandle{
			ID:           provider.InstanceID(id),
			ManifestName: name,
			Provider:     provider.ProviderAWS,
			Region:       "us-east-1",
			State:        provider.StatusRunning,
			CreatedAt:    time.Now().UTC(),
		},
		CredDir: "/tmp/creds",
	}
}

func TestLoad_nonexistent(t *testing.T) {
	r, err := Load(filepath.Join(t.TempDir(), "registry.json"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(r.List()) != 0 {
		t.Error("expected empty registry")
	}
}

func TestAdd_Get_byName(t *testing.T) {
	r, _ := Load(filepath.Join(t.TempDir(), "registry.json"))
	e := makeEntry("my-runner", "i-abc")
	if err := r.Add(e); err != nil {
		t.Fatalf("Add: %v", err)
	}
	got, err := r.Get("my-runner")
	if err != nil {
		t.Fatalf("Get by name: %v", err)
	}
	if string(got.Handle.ID) != "i-abc" {
		t.Errorf("ID = %q, want %q", got.Handle.ID, "i-abc")
	}
}

func TestGet_byID(t *testing.T) {
	r, _ := Load(filepath.Join(t.TempDir(), "registry.json"))
	_ = r.Add(makeEntry("my-runner", "i-abc"))

	got, err := r.Get("i-abc")
	if err != nil {
		t.Fatalf("Get by ID: %v", err)
	}
	if got.Handle.ManifestName != "my-runner" {
		t.Errorf("ManifestName = %q, want %q", got.Handle.ManifestName, "my-runner")
	}
}

func TestGet_notFound(t *testing.T) {
	r, _ := Load(filepath.Join(t.TempDir(), "registry.json"))
	_, err := r.Get("missing")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestRemove(t *testing.T) {
	r, _ := Load(filepath.Join(t.TempDir(), "registry.json"))
	_ = r.Add(makeEntry("my-runner", "i-abc"))

	if err := r.Remove("my-runner"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := r.Get("my-runner"); err == nil {
		t.Fatal("expected error after remove")
	}
}

func TestUpdateExpiry(t *testing.T) {
	r, _ := Load(filepath.Join(t.TempDir(), "registry.json"))
	_ = r.Add(makeEntry("my-runner", "i-abc"))

	exp := time.Now().Add(3 * time.Hour).UTC()
	if err := r.UpdateExpiry("my-runner", &exp); err != nil {
		t.Fatalf("UpdateExpiry: %v", err)
	}
	got, _ := r.Get("my-runner")
	if got.Handle.ExpiresAt == nil || !got.Handle.ExpiresAt.Equal(exp) {
		t.Errorf("ExpiresAt = %v, want %v", got.Handle.ExpiresAt, exp)
	}
	if err := r.UpdateExpiry("nope", &exp); err == nil {
		t.Error("expected error for unknown instance")
	}
}

func TestReload_picksUpExternalWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")
	r1, _ := Load(path)
	_ = r1.Add(makeEntry("my-runner", "i-abc"))

	// A second process (here, a second Registry over the same file) snoozes.
	r2, _ := Load(path)
	exp := time.Now().Add(5 * time.Hour).UTC()
	_ = r2.UpdateExpiry("my-runner", &exp)

	// r1 doesn't see it until it reloads from disk.
	if e, _ := r1.Get("my-runner"); e.Handle.ExpiresAt != nil {
		t.Fatal("r1 should not see the write before Reload")
	}
	if err := r1.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if e, _ := r1.Get("my-runner"); e.Handle.ExpiresAt == nil || !e.Handle.ExpiresAt.Equal(exp) {
		t.Errorf("after Reload ExpiresAt = %v, want %v", e.Handle.ExpiresAt, exp)
	}
}

func TestList(t *testing.T) {
	r, _ := Load(filepath.Join(t.TempDir(), "registry.json"))
	_ = r.Add(makeEntry("runner-a", "i-aaa"))
	_ = r.Add(makeEntry("runner-b", "i-bbb"))

	entries := r.List()
	if len(entries) != 2 {
		t.Errorf("List len = %d, want 2", len(entries))
	}
}

func TestPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")

	r1, _ := Load(path)
	_ = r1.Add(makeEntry("my-runner", "i-abc"))

	r2, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, err := r2.Get("my-runner")
	if err != nil {
		t.Fatalf("Get after reload: %v", err)
	}
	if string(got.Handle.ID) != "i-abc" {
		t.Errorf("ID after reload = %q, want %q", got.Handle.ID, "i-abc")
	}
}

// Two processes hold their own copy of the registry. A write from one must not
// erase what the other wrote in the meantime.
func TestStaleCopyDoesNotEraseOtherWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")
	a, _ := Load(path)
	if err := a.Add(&Entry{Handle: provider.InstanceHandle{ID: "i-a", ManifestName: "a"}}); err != nil {
		t.Fatal(err)
	}
	stale, _ := Load(path) // e.g. a `down` that is now waiting on the cloud
	b, _ := Load(path)
	if err := b.Add(&Entry{Handle: provider.InstanceHandle{ID: "i-b", ManifestName: "b"}}); err != nil {
		t.Fatal(err)
	}
	if err := stale.Remove("a"); err != nil {
		t.Fatal(err)
	}
	fresh, _ := Load(path)
	if _, err := fresh.Get("b"); err != nil {
		t.Error("entry written by another process was erased by a stale copy")
	}
	if _, err := fresh.Get("a"); err == nil {
		t.Error("removed entry is still there")
	}
}

func TestAddNewRefusesTakenName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")
	r, _ := Load(path)
	e := func(id string) *Entry {
		return &Entry{Handle: provider.InstanceHandle{ID: provider.InstanceID(id), ManifestName: "dev"}}
	}
	if err := r.AddNew(e("i-1")); err != nil {
		t.Fatal(err)
	}
	other, _ := Load(path)
	if err := other.AddNew(e("i-2")); !errors.Is(err, ErrExists) {
		t.Fatalf("AddNew on a taken name = %v, want ErrExists", err)
	}
	got, _ := other.Get("dev")
	if got.Handle.ID != "i-1" {
		t.Error("the first instance's entry was overwritten")
	}
}

func TestRemoveIDLeavesReusedName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")
	r, _ := Load(path)
	_ = r.Add(&Entry{Handle: provider.InstanceHandle{ID: "i-new", ManifestName: "dev"}})
	if err := r.RemoveID("i-old"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get("dev"); err != nil {
		t.Error("RemoveID removed an entry with a different ID")
	}
}

func TestConcurrentWritersKeepEveryEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, _ := Load(path)
			name := fmt.Sprintf("m%d", i)
			_ = r.Add(&Entry{Handle: provider.InstanceHandle{ID: provider.InstanceID(name), ManifestName: name}})
		}(i)
	}
	wg.Wait()
	r, _ := Load(path)
	if n := len(r.List()); n != 20 {
		t.Errorf("%d entries survived 20 concurrent writers, want 20", n)
	}
}
