package gcp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"google.golang.org/api/compute/v1"

	"github.com/marshal-org/runlease/pkg/provider"
)

func subnetLink(region, name string) string {
	return "https://www.googleapis.com/compute/v1/projects/proj/regions/" + region + "/subnetworks/" + name
}

func regionAnswer(region string, letters ...string) *compute.Region {
	r := &compute.Region{Name: region}
	for _, l := range letters {
		r.Zones = append(r.Zones, "https://www.googleapis.com/compute/v1/projects/proj/zones/"+region+"-"+l)
	}
	return r
}

func TestResolvePlacement(t *testing.T) {
	usCentral := regionAnswer("us-central1", "c", "a", "b")
	euWest := regionAnswer("europe-west4", "b", "a")
	defaultNet := &compute.Network{Name: "default", AutoCreateSubnetworks: true}
	// A custom-mode network with one subnetwork, in europe-west4.
	vpc := &compute.Network{Name: "vpc", Subnetworks: []string{subnetLink("europe-west4", "apps")}}
	apps := &compute.Subnetwork{Name: "apps", Network: "projects/proj/global/networks/vpc"}
	two := &compute.Network{Name: "two", Subnetworks: []string{subnetLink("europe-west4", "a1"), subnetLink("europe-west4", "a2")}}

	tests := []struct {
		name    string
		zone    string
		req     provider.PlacementRequest
		answers map[string]any

		wantZone, wantNetwork, wantSubnet string
		wantNotes                         int
		wantErr                           error
		wantErrHas                        []string
	}{
		{
			name: "a zone and the default network need nothing filled in", zone: "us-central1-a",
			answers:  map[string]any{"proj/regions/us-central1": usCentral, "proj/global/networks/default": defaultNet},
			wantZone: "us-central1-a",
		},
		{
			name: "a region becomes its first zone", zone: "europe-west4",
			answers:  map[string]any{"proj/regions/europe-west4": euWest, "proj/global/networks/default": defaultNet},
			wantZone: "europe-west4-a", wantNotes: 1,
		},
		{
			name: "no default network: the one subnetwork in the region is used", zone: "europe-west4",
			answers: map[string]any{"proj/regions/europe-west4": euWest,
				"proj/global/networks": &compute.NetworkList{Items: []*compute.Network{vpc}}},
			wantZone: "europe-west4-a", wantSubnet: "apps", wantNotes: 2,
		},
		{
			name: "no default network: the one auto-mode network is used", zone: "us-central1-a",
			answers: map[string]any{"proj/regions/us-central1": usCentral,
				"proj/global/networks": &compute.NetworkList{Items: []*compute.Network{{Name: "main", AutoCreateSubnetworks: true}}}},
			wantZone: "us-central1-a", wantNetwork: "main", wantNotes: 1,
		},
		{
			name: "nobody chose the region: move to the one that has the subnetwork", zone: "us-central1-a",
			req: provider.PlacementRequest{AnyRegion: true},
			answers: map[string]any{"proj/regions/us-central1": usCentral, "proj/regions/europe-west4": euWest,
				"proj/global/networks": &compute.NetworkList{Items: []*compute.Network{vpc}}},
			wantZone: "europe-west4-a", wantSubnet: "apps", wantNotes: 2,
		},
		{
			name: "the user chose the region: say where the subnetwork is instead", zone: "us-central1-a",
			answers: map[string]any{"proj/regions/us-central1": usCentral,
				"proj/global/networks": &compute.NetworkList{Items: []*compute.Network{vpc}}},
			wantErr: provider.ErrNoNetwork, wantErrHas: []string{"no subnetwork in us-central1", "--region europe-west4 --subnet apps"},
		},
		{
			name: "two candidates: list them, do not guess", zone: "europe-west4-b",
			answers: map[string]any{"proj/regions/europe-west4": euWest,
				"proj/global/networks": &compute.NetworkList{Items: []*compute.Network{two}}},
			wantErr: provider.ErrNoNetwork, wantErrHas: []string{"--subnet a1", "--subnet a2"},
		},
		{
			name: "a named custom-mode network gets its one subnetwork", zone: "europe-west4-b",
			req:      provider.PlacementRequest{Network: "vpc"},
			answers:  map[string]any{"proj/regions/europe-west4": euWest, "proj/global/networks/vpc": vpc},
			wantZone: "europe-west4-b", wantNetwork: "vpc", wantSubnet: "apps", wantNotes: 1,
		},
		{
			name: "a named subnetwork is checked, not replaced", zone: "europe-west4-b",
			req:      provider.PlacementRequest{Subnet: "apps"},
			answers:  map[string]any{"proj/regions/europe-west4": euWest, "proj/regions/europe-west4/subnetworks/apps": apps},
			wantZone: "europe-west4-b", wantSubnet: "apps",
		},
		{
			name: "a named subnetwork that is not there", zone: "europe-west4-b",
			req:     provider.PlacementRequest{Subnet: "nope"},
			answers: map[string]any{"proj/regions/europe-west4": euWest},
			wantErr: provider.ErrNoNetwork, wantErrHas: []string{"no subnetwork nope in europe-west4"},
		},
		{
			name: "a named network that is not there lists the ones that are", zone: "europe-west4-b",
			req: provider.PlacementRequest{Network: "nope"},
			answers: map[string]any{"proj/regions/europe-west4": euWest,
				"proj/global/networks": &compute.NetworkList{Items: []*compute.Network{vpc, two}}},
			wantErr: provider.ErrNoNetwork, wantErrHas: []string{"no network named nope", "two, vpc"},
		},
		{
			name: "a project with no network at all", zone: "us-central1-a",
			answers: map[string]any{"proj/regions/us-central1": usCentral, "proj/global/networks": &compute.NetworkList{}},
			wantErr: provider.ErrNoNetwork, wantErrHas: []string{"has no VPC network", "gcloud compute networks create default"},
		},
		{
			name: "a region that does not exist", zone: "europe-west44",
			wantErr: provider.ErrBadRegion, wantErrHas: []string{"no region europe-west44"},
		},
		{
			name: "a zone the region does not have", zone: "europe-west4-z",
			answers: map[string]any{"proj/regions/europe-west4": euWest},
			wantErr: provider.ErrBadRegion, wantErrHas: []string{"no zone europe-west4-z", "europe-west4-a, europe-west4-b"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := preflightFake(t, tc.answers)
			p.zone = tc.zone
			pl, err := p.ResolvePlacement(context.Background(), tc.req)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				for _, has := range tc.wantErrHas {
					if !strings.Contains(err.Error(), has) {
						t.Errorf("err = %q, want it to contain %q", err, has)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolvePlacement: %v", err)
			}
			if pl.Region != tc.wantZone || pl.Network != tc.wantNetwork || pl.Subnet != tc.wantSubnet {
				t.Errorf("got zone %q network %q subnet %q; want %q %q %q",
					pl.Region, pl.Network, pl.Subnet, tc.wantZone, tc.wantNetwork, tc.wantSubnet)
			}
			if p.zone != tc.wantZone {
				t.Errorf("the provider launches in %q, want %q", p.zone, tc.wantZone)
			}
			if len(pl.Notes) != tc.wantNotes {
				t.Errorf("notes = %q, want %d of them", pl.Notes, tc.wantNotes)
			}
		})
	}
}

// A check that cannot be made (here: no permission) is not a verdict on the
// request, so it carries neither sentinel and `up` launches as asked.
func TestResolvePlacement_checkFailing(t *testing.T) {
	p := answerWith(t, 403, `{"error":{"code":403,"message":"Required 'compute.regions.get' permission","errors":[{"reason":"forbidden"}]}}`)
	_, err := p.ResolvePlacement(context.Background(), provider.PlacementRequest{})
	if err == nil || errors.Is(err, provider.ErrBadRegion) || errors.Is(err, provider.ErrNoNetwork) {
		t.Errorf("err = %v, want a plain error", err)
	}
}
