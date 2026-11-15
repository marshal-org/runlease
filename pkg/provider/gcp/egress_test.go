package gcp

import (
	"testing"

	"google.golang.org/api/compute/v1"
)

func TestNATCovers(t *testing.T) {
	allSubnets := &compute.RouterNat{SourceSubnetworkIpRangesToNat: "ALL_SUBNETWORKS_ALL_IP_RANGES"}
	listed := &compute.RouterNat{
		SourceSubnetworkIpRangesToNat: "LIST_OF_SUBNETWORKS",
		Subnetworks: []*compute.RouterNatSubnetworkToNat{
			{Name: "https://www.googleapis.com/compute/v1/projects/p/regions/r/subnetworks/apps"},
		},
	}
	router := func(network string, nats ...*compute.RouterNat) *compute.Router {
		return &compute.Router{Network: "https://www.googleapis.com/compute/v1/projects/p/global/networks/" + network, Nats: nats}
	}

	tests := []struct {
		name    string
		routers []*compute.Router
		network string
		subnet  string
		want    bool
	}{
		{name: "no routers", network: "vpc", subnet: "apps"},
		{name: "router with no NAT", routers: []*compute.Router{router("vpc")}, network: "vpc", subnet: "apps"},
		{name: "NAT for the whole network", routers: []*compute.Router{router("vpc", allSubnets)}, network: "vpc", subnet: "apps", want: true},
		{name: "NAT on another network", routers: []*compute.Router{router("other", allSubnets)}, network: "vpc", subnet: "apps"},
		{name: "NAT lists this subnetwork", routers: []*compute.Router{router("vpc", listed)}, network: "global/networks/vpc", subnet: "apps", want: true},
		{name: "NAT lists a different subnetwork", routers: []*compute.Router{router("vpc", listed)}, network: "vpc", subnet: "db"},
		{name: "empty network means default", routers: []*compute.Router{router("default", allSubnets)}, subnet: "default", want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := natCovers(tc.routers, tc.network, tc.subnet); got != tc.want {
				t.Errorf("natCovers = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSubnetLocation(t *testing.T) {
	p := &Provider{projectID: "mine", zone: "europe-west1-b"}
	tests := []struct {
		subnet                    string
		project, region, wantName string
	}{
		{"", "mine", "europe-west1", "default"},
		{"apps", "mine", "europe-west1", "apps"},
		{"regions/us-east1/subnetworks/apps", "mine", "us-east1", "apps"},
		{"projects/host/regions/us-east1/subnetworks/shared", "host", "us-east1", "shared"},
	}
	for _, tc := range tests {
		project, region, name := p.subnetLocation(tc.subnet)
		if project != tc.project || region != tc.region || name != tc.wantName {
			t.Errorf("subnetLocation(%q) = %s, %s, %s; want %s, %s, %s",
				tc.subnet, project, region, name, tc.project, tc.region, tc.wantName)
		}
	}
}
