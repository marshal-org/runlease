package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/api/compute/v1"
	"google.golang.org/api/option"

	"github.com/runlease/runlease/pkg/provider"
)

// fakeCompute is a minimal in-memory Compute Engine API: instances keyed by
// "project/zone/name". Delete removes the instance unless sticky is set (the
// op still reports DONE), which models "delete op finished, VM still there".
type fakeCompute struct {
	mu        sync.Mutex
	instances map[string]string // key → status
	sticky    bool
	getErrors int      // fail this many GETs with a 503 before answering
	calls     []string // "METHOD project/zone/name"
}

func (f *fakeCompute) handler(w http.ResponseWriter, r *http.Request) {
	// Paths: /compute/v1/projects/{p}/zones/{z}/{instances|operations}/{name}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/compute/v1/"), "/")
	if len(parts) < 5 || parts[0] != "projects" || parts[2] != "zones" {
		http.Error(w, "bad path "+r.URL.Path, http.StatusBadRequest)
		return
	}
	project, zone := parts[1], parts[3]
	f.mu.Lock()
	defer f.mu.Unlock()

	if parts[4] == "operations" {
		writeJSON(w, &compute.Operation{Name: parts[5], Status: "DONE"})
		return
	}
	switch {
	case parts[4] == "instances" && len(parts) == 5 && r.Method == http.MethodPost:
		var inst compute.Instance
		_ = json.NewDecoder(r.Body).Decode(&inst)
		key := project + "/" + zone + "/" + inst.Name
		f.calls = append(f.calls, "INSERT "+key)
		f.instances[key] = "RUNNING"
		writeJSON(w, &compute.Operation{Name: "op-insert", Status: "DONE"})
	case parts[4] == "instances" && len(parts) == 6:
		key := project + "/" + zone + "/" + parts[5]
		f.calls = append(f.calls, r.Method+" "+key)
		if r.Method == http.MethodGet && f.getErrors > 0 {
			f.getErrors--
			w.WriteHeader(http.StatusServiceUnavailable)
			writeJSON(w, map[string]any{"error": map[string]any{"code": 503, "message": "backend error"}})
			return
		}
		status, ok := f.instances[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			writeJSON(w, map[string]any{"error": map[string]any{"code": 404, "message": "not found"}})
			return
		}
		if r.Method == http.MethodDelete {
			if !f.sticky {
				delete(f.instances, key)
			}
			writeJSON(w, &compute.Operation{Name: "op-delete", Status: "DONE"})
			return
		}
		writeJSON(w, &compute.Instance{Name: parts[5], Status: status})
	default:
		http.Error(w, "unhandled "+r.Method+" "+r.URL.Path, http.StatusBadRequest)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func newFake(t *testing.T, instances map[string]string) (*fakeCompute, *compute.Service) {
	t.Helper()
	f := &fakeCompute{instances: instances}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	svc, err := compute.NewService(context.Background(),
		option.WithEndpoint(srv.URL+"/compute/v1/"),
		option.WithHTTPClient(srv.Client()),
		option.WithoutAuthentication())
	if err != nil {
		t.Fatalf("compute.NewService: %v", err)
	}
	return f, svc
}

func testProvider(svc *compute.Service, project, zone string) *Provider {
	return &Provider{svc: svc, projectID: project, zone: zone,
		pollInterval: time.Millisecond, deleteTimeout: 50 * time.Millisecond}
}

func TestRequestSpot_recordsProjectInHandle(t *testing.T) {
	_, svc := newFake(t, map[string]string{})
	p := testProvider(svc, "proj-a", "europe-west1-b")
	h, err := p.RequestSpot(context.Background(), provider.InstanceSpec{ManifestName: "x", InstanceType: "e2-small"})
	if err != nil {
		t.Fatalf("RequestSpot: %v", err)
	}
	if h.Project != "proj-a" {
		t.Fatalf("handle.Project = %q, want proj-a", h.Project)
	}
}

func TestTerminate_deletesInStoredProjectAndConfirmsGone(t *testing.T) {
	f, svc := newFake(t, map[string]string{"proj-a/europe-west1-b/rl-x": "RUNNING"})
	if err := testProvider(svc, "proj-a", "europe-west1-b").Terminate(context.Background(), "rl-x"); err != nil {
		t.Fatalf("Terminate: %v", err)
	}
	if len(f.instances) != 0 {
		t.Fatalf("instance still present: %v", f.instances)
	}
	want := []string{"DELETE proj-a/europe-west1-b/rl-x", "GET proj-a/europe-west1-b/rl-x"}
	if strings.Join(f.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("calls = %v, want %v", f.calls, want)
	}
}

// The reported bug: the VM lives in proj-a, the provider points at proj-b,
// Delete 404s — that must not read as success.
func TestTerminate_notFoundInWrongProjectIsNotSuccess(t *testing.T) {
	f, svc := newFake(t, map[string]string{"proj-a/europe-west1-b/rl-x": "RUNNING"})
	err := testProvider(svc, "proj-b", "europe-west1-b").Terminate(context.Background(), "rl-x")
	if !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if !strings.Contains(err.Error(), "proj-b") || !strings.Contains(err.Error(), "europe-west1-b") {
		t.Fatalf("error should name the project and zone looked in: %v", err)
	}
	if _, ok := f.instances["proj-a/europe-west1-b/rl-x"]; !ok {
		t.Fatal("instance in proj-a should be untouched")
	}
}

func TestTerminate_instanceStillPresentAfterDeleteOpIsError(t *testing.T) {
	f, svc := newFake(t, map[string]string{"proj-a/europe-west1-b/rl-x": "STOPPING"})
	f.sticky = true
	err := testProvider(svc, "proj-a", "europe-west1-b").Terminate(context.Background(), "rl-x")
	if err == nil || errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("err = %v, want a still-exists error", err)
	}
	if !strings.Contains(err.Error(), "still exists") {
		t.Fatalf("err = %v, want it to say the instance still exists", err)
	}
}

func TestTerminate_retriesTransientGetErrors(t *testing.T) {
	f, svc := newFake(t, map[string]string{"proj-a/europe-west1-b/rl-x": "RUNNING"})
	f.getErrors = 2
	if err := testProvider(svc, "proj-a", "europe-west1-b").Terminate(context.Background(), "rl-x"); err != nil {
		t.Fatalf("Terminate should ride out transient Get errors: %v", err)
	}
}

func TestTerminate_persistentGetErrorFailsAtDeadline(t *testing.T) {
	f, svc := newFake(t, map[string]string{"proj-a/europe-west1-b/rl-x": "RUNNING"})
	f.getErrors = 1 << 20
	err := testProvider(svc, "proj-a", "europe-west1-b").Terminate(context.Background(), "rl-x")
	if err == nil || !strings.Contains(err.Error(), "confirming deletion") {
		t.Fatalf("err = %v, want a confirming-deletion error", err)
	}
}
