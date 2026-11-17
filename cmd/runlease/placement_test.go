package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/marshal-org/runlease/pkg/manifest"
	"github.com/marshal-org/runlease/pkg/provider"
)

// fakePlacer is a cloud whose placement answer is set by the test.
type fakePlacer struct {
	fakePreflight
	placement *provider.Placement
	placeErr  error
	got       provider.PlacementRequest
}

func (f *fakePlacer) ResolvePlacement(_ context.Context, req provider.PlacementRequest) (*provider.Placement, error) {
	f.got = req
	return f.placement, f.placeErr
}

func TestResolvePlacement(t *testing.T) {
	ambiguous := fmt.Errorf("%w: project p has 2 places a machine could join in europe-west4, so name one:\n  --subnet a1\n  --subnet a2", provider.ErrNoNetwork)
	tests := []struct {
		name       string
		placement  *provider.Placement
		placeErr   error
		wantErrHas string
		wantRegion string
		wantSubnet string
		wantOut    string
		wantWarn   bool
	}{
		{
			name:       "what the provider chose is written back and said",
			placement:  &provider.Placement{Region: "europe-west4-a", Subnet: "apps", Notes: []string{"zone europe-west4-a", "subnetwork apps"}},
			wantRegion: "europe-west4-a", wantSubnet: "apps", wantOut: "📍 Using zone europe-west4-a\n📍 Using subnetwork apps\n",
		},
		{name: "a network that cannot work stops the launch", placeErr: ambiguous, wantErrHas: "--subnet a2\n\nNothing was created", wantRegion: "europe-west4"},
		{name: "a region that does not exist stops the launch", placeErr: fmt.Errorf("%w: no region x", provider.ErrBadRegion), wantErrHas: "Nothing was created", wantRegion: "europe-west4"},
		{name: "an expired sign-in is reported as that", placeErr: errors.New("oauth2: cannot fetch token: invalid_grant"), wantErrHas: "invalid_grant", wantRegion: "europe-west4"},
		{name: "a check that cannot be made does not block the launch", placeErr: errors.New("googleapi: Error 403: Required 'compute.regions.get' permission"), wantRegion: "europe-west4", wantWarn: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			quickRegionDefaulted = true
			t.Cleanup(func() { quickRegionDefaulted = false })
			p := &fakePlacer{placement: tc.placement, placeErr: tc.placeErr}
			m := &manifest.Manifest{Provider: "gcp", Region: "europe-west4"}
			var out, errOut bytes.Buffer
			err := resolvePlacement(context.Background(), &out, &errOut, p, m)
			if tc.wantErrHas == "" && err != nil {
				t.Fatalf("err = %v", err)
			}
			if tc.wantErrHas != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErrHas)) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErrHas)
			}
			if !p.got.AnyRegion {
				t.Error("a region nobody chose must be passed on as AnyRegion")
			}
			if m.Region != tc.wantRegion || m.GCP.Subnetwork != tc.wantSubnet {
				t.Errorf("manifest region %q subnetwork %q, want %q %q", m.Region, m.GCP.Subnetwork, tc.wantRegion, tc.wantSubnet)
			}
			if out.String() != tc.wantOut {
				t.Errorf("printed %q, want %q", out.String(), tc.wantOut)
			}
			if got := errOut.Len() > 0; got != tc.wantWarn {
				t.Errorf("warned = %v, want %v (%q)", got, tc.wantWarn, errOut.String())
			}
		})
	}
}

// A provider with no placement step launches exactly as asked.
func TestResolvePlacement_providerWithout(t *testing.T) {
	m := &manifest.Manifest{Provider: "aws", Region: "us-east-1"}
	if err := resolvePlacement(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, &fakeEgressProvider{}, m); err != nil || m.Region != "us-east-1" {
		t.Errorf("err = %v, region %q", err, m.Region)
	}
}

// doctor resolves the placement the way up does, so it passes what a launch
// would fill in and fails, with the same choices, what a launch would refuse.
func TestDoctorCloud_placement(t *testing.T) {
	target := doctorTarget{provider: "gcp", region: "europe-west4", regionWhy: "your saved default"}

	picked := &fakePlacer{placement: &provider.Placement{Region: "europe-west4-a", Subnet: "apps"}}
	picked.has, picked.network = true, "subnetwork apps (network vpc, europe-west4)"
	checks := runCloud(picked, nil, target)
	if got := ids(checks); got != "gcp.credentials=ok gcp.project=ok gcp.region=ok gcp.network=ok gcp.nat=ok" {
		t.Errorf("checks = %s", got)
	}
	if d := checksByID(checks)["gcp.region"].Detail; !strings.Contains(d, "europe-west4-a") || !strings.Contains(d, "picked by runlease; you had europe-west4") {
		t.Errorf("region detail = %q", d)
	}

	ambiguous := &fakePlacer{placeErr: fmt.Errorf("%w: project p has 2 places a machine could join, so name one:\n  --subnet a1\n  --subnet a2", provider.ErrNoNetwork)}
	checks = runCloud(ambiguous, nil, target)
	if got := ids(checks); got != "gcp.credentials=ok gcp.project=ok gcp.region=ok gcp.network=fail" {
		t.Errorf("checks = %s", got)
	}
	if n := checksByID(checks)["gcp.network"]; !strings.Contains(n.Detail, "2 places") || !strings.Contains(n.Fix, "--subnet a2") {
		t.Errorf("network check = %+v", n)
	}

	badRegion := &fakePlacer{placeErr: fmt.Errorf("%w: Google Cloud has no region europe-west44", provider.ErrBadRegion)}
	checks = runCloud(badRegion, nil, target)
	if got := ids(checks); got != "gcp.credentials=ok gcp.project=ok gcp.region=fail" {
		t.Errorf("checks = %s", got)
	}
}
