package gcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/api/compute/v1"
	"google.golang.org/api/option"

	"github.com/marshal-org/runlease/pkg/provider"
)

// preflightFake answers GETs by path (below /compute/v1/projects/) and 404s
// the rest.
func preflightFake(t *testing.T, answers map[string]any) *Provider {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/compute/v1/projects/")
		if v, ok := answers[path]; ok {
			writeJSON(w, v)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, map[string]any{"error": map[string]any{"code": 404, "message": "not found"}})
	}))
	t.Cleanup(srv.Close)
	svc, err := compute.NewService(context.Background(),
		option.WithEndpoint(srv.URL+"/compute/v1/"), option.WithHTTPClient(srv.Client()), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	return testProvider(svc, "proj", "us-central1-a")
}

func TestCheckNetwork(t *testing.T) {
	answers := map[string]any{
		"proj/global/networks/default": &compute.Network{Name: "default", AutoCreateSubnetworks: true},
		"proj/global/networks/custom": &compute.Network{Name: "custom", Subnetworks: []string{
			"https://www.googleapis.com/compute/v1/projects/proj/regions/us-central1/subnetworks/apps"}},
		"proj/global/networks/elsewhere": &compute.Network{Name: "elsewhere", Subnetworks: []string{
			"https://www.googleapis.com/compute/v1/projects/proj/regions/europe-west1/subnetworks/eu"}},
		"proj/regions/us-central1/subnetworks/apps": &compute.Subnetwork{Name: "apps", Network: "projects/proj/global/networks/custom"},
		"host/regions/us-east1/subnetworks/shared":  &compute.Subnetwork{Name: "shared", Network: "projects/host/global/networks/vpc"},
	}
	tests := []struct {
		name, network, subnet string
		want                  string
		wantErrHas            string
	}{
		{name: "default network", want: "network default (us-central1)"},
		{name: "named subnetwork", subnet: "apps", want: "subnetwork apps (network custom, us-central1)"},
		{name: "shared VPC self-link", subnet: "projects/host/regions/us-east1/subnetworks/shared", want: "subnetwork shared (network vpc, us-east1)"},
		{name: "missing network", network: "nope", wantErrHas: "no network named nope"},
		{name: "missing subnetwork", subnet: "nope", wantErrHas: "no subnetwork nope in us-central1"},
		{name: "custom-mode network needs a subnetwork", network: "custom", wantErrHas: "it has apps in us-central1"},
		{name: "custom-mode network with nothing in the region", network: "elsewhere", wantErrHas: "no subnetwork in us-central1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := preflightFake(t, answers).CheckNetwork(context.Background(), tc.network, tc.subnet)
			if tc.wantErrHas != "" {
				if !errors.Is(err, provider.ErrNoNetwork) || !strings.Contains(err.Error(), tc.wantErrHas) {
					t.Fatalf("err = %v, want ErrNoNetwork containing %q", err, tc.wantErrHas)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Errorf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestCheckNetwork_noDefaultNetwork(t *testing.T) {
	_, err := preflightFake(t, nil).CheckNetwork(context.Background(), "", "")
	if !errors.Is(err, provider.ErrNoNetwork) || !strings.Contains(err.Error(), "no network named default") {
		t.Errorf("err = %v", err)
	}
}

func TestCheckAccessAndRegion(t *testing.T) {
	p := preflightFake(t, map[string]any{
		"proj":                     &compute.Project{Name: "proj"},
		"proj/zones/us-central1-a": &compute.Zone{Name: "us-central1-a"},
	})
	if err := p.CheckAccess(context.Background()); err != nil {
		t.Errorf("CheckAccess: %v", err)
	}
	if err := p.CheckRegion(context.Background()); err != nil {
		t.Errorf("CheckRegion: %v", err)
	}
	p.zone = "us-central1"
	if err := p.CheckRegion(context.Background()); err == nil || !strings.Contains(err.Error(), "is not a zone") {
		t.Errorf("a region where a zone is needed: err = %v", err)
	}
	p.projectID = "gone"
	if err := p.CheckAccess(context.Background()); err == nil {
		t.Error("CheckAccess on a project that is not there should fail")
	}
}

var _ provider.Preflighter = (*Provider)(nil)
