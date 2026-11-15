package main

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/marshal-org/runlease/pkg/manifest"
)

func TestSubnetForProvider(t *testing.T) {
	tests := []struct {
		name string
		m    *manifest.Manifest
		want string
	}{
		{
			name: "gcp reads gcp.subnetwork",
			m: &manifest.Manifest{
				Provider: "gcp",
				GCP:      manifest.GCPConfig{Subnetwork: "my-subnet"},
				AWS:      manifest.AWSConfig{SubnetID: "subnet-should-be-ignored"},
			},
			want: "my-subnet",
		},
		{
			name: "aws reads aws.subnetID",
			m: &manifest.Manifest{
				Provider: "aws",
				AWS:      manifest.AWSConfig{SubnetID: "subnet-123"},
				GCP:      manifest.GCPConfig{Subnetwork: "should-be-ignored"},
			},
			want: "subnet-123",
		},
		{
			name: "gcp with no subnetwork is empty (auto-mode default)",
			m:    &manifest.Manifest{Provider: "gcp"},
			want: "",
		},
		{
			name: "other providers have no subnet",
			m:    &manifest.Manifest{Provider: "azure"},
			want: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := subnetForProvider(tc.m); got != tc.want {
				t.Errorf("subnetForProvider() = %q, want %q", got, tc.want)
			}
		})
	}
}

// resetQuickFlags restores the package-level quick-mode flag vars to the
// defaults cobra assigns in init(), so each subtest starts clean.
func resetQuickFlags(t *testing.T) {
	t.Helper()
	quickProvider = ""
	quickRegion = ""
	quickSize = "compute.small"
	quickDiskSize = 0
	quickImage = ""
	quickRelay = ""
	quickTTL = "2h"
	quickName = ""
	quickSelfTerminate = true
	quickServiceAccount = ""
	quickInstanceProfile = ""
	quickNetwork = ""
	quickSubnet = ""
}

func TestBuildQuickManifest_networkRouting(t *testing.T) {
	// Hermetic: no ~/.runlease/config.yaml bleeds into the built manifest.
	t.Setenv("RUNLEASE_HOME", t.TempDir())

	t.Run("gcp routes --network and --subnet to gcp block", func(t *testing.T) {
		resetQuickFlags(t)
		quickProvider = "gcp"
		quickNetwork = "my-vpc"
		quickSubnet = "my-subnet"

		m, err := buildQuickManifest()
		if err != nil {
			t.Fatalf("buildQuickManifest: %v", err)
		}
		if m.GCP.Network != "my-vpc" {
			t.Errorf("GCP.Network = %q, want my-vpc", m.GCP.Network)
		}
		if m.GCP.Subnetwork != "my-subnet" {
			t.Errorf("GCP.Subnetwork = %q, want my-subnet", m.GCP.Subnetwork)
		}
		if m.AWS.SubnetID != "" {
			t.Errorf("AWS.SubnetID = %q, want empty", m.AWS.SubnetID)
		}
		if got := subnetForProvider(m); got != "my-subnet" {
			t.Errorf("subnetForProvider = %q, want my-subnet", got)
		}
	})

	t.Run("aws routes --subnet to aws block", func(t *testing.T) {
		resetQuickFlags(t)
		quickProvider = "aws"
		quickSubnet = "subnet-abc"

		m, err := buildQuickManifest()
		if err != nil {
			t.Fatalf("buildQuickManifest: %v", err)
		}
		if m.AWS.SubnetID != "subnet-abc" {
			t.Errorf("AWS.SubnetID = %q, want subnet-abc", m.AWS.SubnetID)
		}
		if m.GCP.Subnetwork != "" {
			t.Errorf("GCP.Subnetwork = %q, want empty", m.GCP.Subnetwork)
		}
		if got := subnetForProvider(m); got != "subnet-abc" {
			t.Errorf("subnetForProvider = %q, want subnet-abc", got)
		}
	})

	t.Run("gcp without network flags leaves both empty", func(t *testing.T) {
		resetQuickFlags(t)
		quickProvider = "gcp"

		m, err := buildQuickManifest()
		if err != nil {
			t.Fatalf("buildQuickManifest: %v", err)
		}
		if m.GCP.Network != "" || m.GCP.Subnetwork != "" {
			t.Errorf("expected empty network/subnetwork, got network=%q subnetwork=%q", m.GCP.Network, m.GCP.Subnetwork)
		}
	})

	t.Run("--network with aws is rejected", func(t *testing.T) {
		resetQuickFlags(t)
		quickProvider = "aws"
		quickNetwork = "my-vpc"

		if _, err := buildQuickManifest(); err == nil {
			t.Fatal("expected error for --network with aws, got nil")
		}
	})
}

type fakeEgressProvider struct {
	fakeDownProvider
	has    bool
	err    error
	called bool
}

func (f *fakeEgressProvider) HasPrivateEgress(context.Context, string, string) (bool, error) {
	f.called = true
	return f.has, f.err
}

func TestCheckPrivateEgress(t *testing.T) {
	yes, no := true, false
	const relayAddr = "wss://relay.example.com"
	tests := []struct {
		name       string
		relay      string
		publicIP   *bool
		has        bool
		checkErr   error
		wantErr    bool
		wantCalled bool
		wantNote   bool
	}{
		{name: "no relay: nothing to check"},
		{name: "relay, default, NAT present", relay: relayAddr, has: true, wantCalled: true},
		{name: "relay, default, no NAT: refuse", relay: relayAddr, wantCalled: true, wantErr: true},
		{name: "relay, default, check failed: warn and go on", relay: relayAddr, checkErr: errors.New("403"), wantCalled: true, wantNote: true},
		{name: "relay, publicIp true: has a way out", relay: relayAddr, publicIP: &yes},
		{name: "relay, publicIp false set on purpose: warn, do not check", relay: relayAddr, publicIP: &no, wantNote: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &fakeEgressProvider{has: tc.has, err: tc.checkErr}
			m := &manifest.Manifest{PublicIP: tc.publicIP, Relay: manifest.RelayConfig{Addr: tc.relay}, Region: "us-central1-a"}
			var errOut bytes.Buffer
			err := checkPrivateEgress(context.Background(), &errOut, p, m)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if p.called != tc.wantCalled {
				t.Errorf("NAT check called = %v, want %v", p.called, tc.wantCalled)
			}
			if got := errOut.Len() > 0; got != tc.wantNote {
				t.Errorf("printed a note = %v, want %v (%q)", got, tc.wantNote, errOut.String())
			}
		})
	}
}
