package gcp

import "testing"

func TestGCPNetworkInterface(t *testing.T) {
	tests := []struct {
		name       string
		network    string
		subnetwork string
		publicIP   bool
		wantNet    string
		wantSubnet string
		wantNAT    bool
	}{
		{
			name: "defaults to auto-mode network (both empty)",
		},
		{
			name:       "subnetwork only — custom-mode VPC",
			subnetwork: "projects/p/regions/europe-west1/subnetworks/my-subnet",
			wantSubnet: "projects/p/regions/europe-west1/subnetworks/my-subnet",
		},
		{
			name:       "network and subnetwork both set",
			network:    "my-vpc",
			subnetwork: "my-subnet",
			wantNet:    "my-vpc",
			wantSubnet: "my-subnet",
		},
		{
			name:     "public IP adds a one-to-one NAT access config",
			publicIP: true,
			wantNAT:  true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ni := gcpNetworkInterface(tc.network, tc.subnetwork, tc.publicIP)
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
