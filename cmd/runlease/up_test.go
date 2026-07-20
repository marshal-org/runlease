package main

import (
	"testing"

	"github.com/runlease/runlease/pkg/manifest"
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
}
