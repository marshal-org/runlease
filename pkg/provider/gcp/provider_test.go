package gcp

import "testing"

func TestGCPNetworkInterface(t *testing.T) {
	tests := []struct {
		name       string
		network    string
		subnetwork string
		zone       string
		publicIP   bool
		wantNet    string
		wantSubnet string
		wantNAT    bool
	}{
		{
			name: "defaults to auto-mode network (both empty)",
		},
		{
			name:       "full self-link subnetwork passes through",
			subnetwork: "projects/p/regions/europe-west1/subnetworks/my-subnet",
			zone:       "europe-west1-b",
			wantSubnet: "projects/p/regions/europe-west1/subnetworks/my-subnet",
		},
		{
			name:       "bare subnetwork name qualified with region from zone",
			subnetwork: "my-subnet",
			zone:       "europe-west1-b",
			wantSubnet: "regions/europe-west1/subnetworks/my-subnet",
		},
		{
			name:       "bare subnetwork with no zone passes through unqualified",
			subnetwork: "my-subnet",
			wantSubnet: "my-subnet",
		},
		{
			name:       "bare network name qualified to global path",
			network:    "my-vpc",
			subnetwork: "my-subnet",
			zone:       "us-central1-a",
			wantNet:    "global/networks/my-vpc",
			wantSubnet: "regions/us-central1/subnetworks/my-subnet",
		},
		{
			name:    "full self-link network passes through",
			network: "projects/p/global/networks/my-vpc",
			wantNet: "projects/p/global/networks/my-vpc",
		},
		{
			name:     "public IP adds a one-to-one NAT access config",
			publicIP: true,
			wantNAT:  true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ni := gcpNetworkInterface(tc.network, tc.subnetwork, tc.zone, tc.publicIP)
			if ni.Network != tc.wantNet {
				t.Errorf("Network = %q, want %q", ni.Network, tc.wantNet)
			}
			if ni.Subnetwork != tc.wantSubnet {
				t.Errorf("Subnetwork = %q, want %q", ni.Subnetwork, tc.wantSubnet)
			}
			gotNAT := len(ni.AccessConfigs) > 0
			if gotNAT != tc.wantNAT {
				t.Errorf("has AccessConfigs = %v, want %v", gotNAT, tc.wantNAT)
			}
		})
	}
}
