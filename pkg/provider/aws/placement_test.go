package aws

import (
	"context"
	"errors"
	"strings"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"

	"github.com/marshal-org/runlease/pkg/provider"
)

func TestSplitZone(t *testing.T) {
	for in, want := range map[string][2]string{
		"eu-north-1":     {"eu-north-1", ""},
		"eu-north-1a":    {"eu-north-1", "eu-north-1a"},
		"us-gov-west-1b": {"us-gov-west-1", "us-gov-west-1b"},
		"us-central1-a":  {"us-central1-a", ""},
	} {
		if region, zone := SplitZone(in); region != want[0] || zone != want[1] {
			t.Errorf("SplitZone(%q) = %q, %q; want %q, %q", in, region, zone, want[0], want[1])
		}
	}
}

func subnet(id, vpc, zone string) types.Subnet {
	return types.Subnet{SubnetId: awssdk.String(id), VpcId: awssdk.String(vpc), AvailabilityZone: awssdk.String(zone)}
}

// table is a route table whose default route leaves through gateway
// (igw-..., nat-...), used by the named subnets, or as the main table of vpc
// when none is named.
func table(vpc, gateway string, subnets ...string) types.RouteTable {
	r := types.Route{DestinationCidrBlock: awssdk.String("0.0.0.0/0"), State: types.RouteStateActive}
	switch {
	case strings.HasPrefix(gateway, "igw-"):
		r.GatewayId = awssdk.String(gateway)
	case gateway != "":
		r.NatGatewayId = awssdk.String(gateway)
	}
	rt := types.RouteTable{VpcId: awssdk.String(vpc), Routes: []types.Route{r}}
	for _, s := range subnets {
		rt.Associations = append(rt.Associations, types.RouteTableAssociation{SubnetId: awssdk.String(s)})
	}
	if len(subnets) == 0 {
		rt.Associations = []types.RouteTableAssociation{{Main: awssdk.Bool(true)}}
	}
	return rt
}

// region is one region's answers: its default VPC's subnets, every subnet,
// and an error for all of it.
type region struct {
	defaults, all []types.Subnet
	tables        []types.RouteTable
	err           error
}

func (r region) mock() *mockEC2 {
	return &mockEC2{
		subnetsFn: func(in *ec2.DescribeSubnetsInput) (*ec2.DescribeSubnetsOutput, error) {
			switch {
			case r.err != nil:
				return nil, r.err
			case len(in.SubnetIds) > 0:
				for _, sn := range append(r.all, r.defaults...) {
					if awssdk.ToString(sn.SubnetId) == in.SubnetIds[0] {
						return &ec2.DescribeSubnetsOutput{Subnets: []types.Subnet{sn}}, nil
					}
				}
				return nil, &smithy.GenericAPIError{Code: "InvalidSubnetID.NotFound", Message: "nope"}
			case len(in.Filters) > 0:
				return &ec2.DescribeSubnetsOutput{Subnets: r.defaults}, nil
			}
			return &ec2.DescribeSubnetsOutput{Subnets: append(r.all, r.defaults...)}, nil
		},
		routeTables: map[string]*ec2.DescribeRouteTablesOutput{"": {RouteTables: r.tables}},
	}
}

func TestResolvePlacement(t *testing.T) {
	denied := &smithy.GenericAPIError{Code: "UnauthorizedOperation", Message: "with an explicit deny in a service control policy"}
	defaultVPC := region{defaults: []types.Subnet{subnet("subnet-a", "vpc-d", "eu-north-1a"), subnet("subnet-b", "vpc-d", "eu-north-1b")}}
	blocked := region{err: denied}

	tests := []struct {
		name    string
		at      string // what the user gave as the region
		req     provider.PlacementRequest
		regions map[string]region

		wantRegion, wantSubnet string
		wantNotes              int
		wantErr                error
		wantErrHas             []string
		wantPlainErr           bool
	}{
		{
			name: "a default VPC needs nothing filled in", at: "eu-north-1",
			regions:    map[string]region{"eu-north-1": defaultVPC},
			wantRegion: "eu-north-1",
		},
		{
			name: "a zone becomes its region and the default subnet there", at: "eu-north-1b",
			regions:    map[string]region{"eu-north-1": defaultVPC},
			wantRegion: "eu-north-1", wantSubnet: "subnet-b", wantNotes: 1,
		},
		{
			name: "a zone the default VPC has no subnet in", at: "eu-north-1c",
			regions: map[string]region{"eu-north-1": defaultVPC},
			wantErr: provider.ErrNoNetwork, wantErrHas: []string{"no subnet in eu-north-1c", "eu-north-1a, eu-north-1b"},
		},
		{
			name: "nobody chose the blocked region: move to the only usable one", at: "us-east-1",
			req:        provider.PlacementRequest{AnyRegion: true},
			regions:    map[string]region{"us-east-1": blocked, "eu-north-1": defaultVPC},
			wantRegion: "eu-north-1", wantNotes: 1,
		},
		{
			name: "the user chose the blocked region: say which may be used", at: "us-east-1",
			regions: map[string]region{"us-east-1": blocked, "eu-north-1": defaultVPC},
			wantErr: provider.ErrBadRegion, wantErrHas: []string{"may not use us-east-1", "It may use: eu-north-1"},
		},
		{
			name: "two usable regions: list them, do not guess", at: "us-east-1",
			req:     provider.PlacementRequest{AnyRegion: true},
			regions: map[string]region{"us-east-1": blocked, "eu-north-1": defaultVPC, "eu-west-1": defaultVPC},
			wantErr: provider.ErrBadRegion, wantErrHas: []string{"It may use: eu-north-1, eu-west-1"},
		},
		{
			name: "denied everywhere is not a verdict on the region", at: "us-east-1",
			req:          provider.PlacementRequest{AnyRegion: true},
			regions:      map[string]region{"us-east-1": blocked},
			wantPlainErr: true,
		},
		{
			name: "a region that does not exist", at: "eu-north-9",
			regions: map[string]region{"eu-north-9": {err: errors.New(`dial tcp: lookup ec2.eu-north-9.amazonaws.com: no such host`)}},
			wantErr: provider.ErrBadRegion, wantErrHas: []string{"no region eu-north-9"},
		},
		{
			name: "no default VPC: a public subnet for a machine with a public IP", at: "eu-north-1",
			req: provider.PlacementRequest{PublicIP: true},
			regions: map[string]region{"eu-north-1": {
				all:    []types.Subnet{subnet("subnet-priv", "vpc-1", "eu-north-1a"), subnet("subnet-pub", "vpc-1", "eu-north-1b"), subnet("subnet-dead", "vpc-1", "eu-north-1c")},
				tables: []types.RouteTable{table("vpc-1", "", "subnet-dead"), table("vpc-1", "nat-1", "subnet-priv"), table("vpc-1", "igw-1", "subnet-pub")},
			}},
			wantRegion: "eu-north-1", wantSubnet: "subnet-pub", wantNotes: 1,
		},
		{
			name: "no default VPC: a NAT subnet for a machine without one", at: "eu-north-1",
			regions: map[string]region{"eu-north-1": {
				all:    []types.Subnet{subnet("subnet-priv", "vpc-1", "eu-north-1a"), subnet("subnet-pub", "vpc-1", "eu-north-1b")},
				tables: []types.RouteTable{table("vpc-1", "nat-1", "subnet-priv"), table("vpc-1", "igw-1")},
			}},
			wantRegion: "eu-north-1", wantSubnet: "subnet-priv", wantNotes: 1,
		},
		{
			name: "no default VPC and only public subnets: still picked, for the NAT check to explain", at: "eu-north-1",
			regions: map[string]region{"eu-north-1": {
				all:    []types.Subnet{subnet("subnet-pub", "vpc-1", "eu-north-1b")},
				tables: []types.RouteTable{table("vpc-1", "igw-1")},
			}},
			wantRegion: "eu-north-1", wantSubnet: "subnet-pub", wantNotes: 1,
		},
		{
			name: "no default VPC and two VPCs: list the subnets, do not guess", at: "eu-north-1",
			req: provider.PlacementRequest{PublicIP: true},
			regions: map[string]region{"eu-north-1": {
				all:    []types.Subnet{subnet("subnet-1", "vpc-1", "eu-north-1a"), subnet("subnet-2", "vpc-2", "eu-north-1a")},
				tables: []types.RouteTable{table("vpc-1", "igw-1"), table("vpc-2", "igw-2")},
			}},
			wantErr: provider.ErrNoNetwork, wantErrHas: []string{"--subnet subnet-1", "--subnet subnet-2"},
		},
		{
			name: "no VPC at all", at: "eu-north-1",
			regions: map[string]region{"eu-north-1": {}},
			wantErr: provider.ErrNoNetwork, wantErrHas: []string{"aws ec2 create-default-vpc --region eu-north-1"},
		},
		{
			name: "subnets, but none with a way out", at: "eu-north-1",
			regions: map[string]region{"eu-north-1": {
				all: []types.Subnet{subnet("subnet-dead", "vpc-1", "eu-north-1a")}, tables: []types.RouteTable{table("vpc-1", "")}}},
			wantErr: provider.ErrNoNetwork, wantErrHas: []string{"none of the 1 subnets", "create-default-vpc"},
		},
		{
			name: "a named subnet is checked, not replaced", at: "eu-north-1",
			req:        provider.PlacementRequest{Subnet: "subnet-a"},
			regions:    map[string]region{"eu-north-1": defaultVPC},
			wantRegion: "eu-north-1", wantSubnet: "subnet-a",
		},
		{
			name: "a named subnet that is not there", at: "eu-north-1",
			req:     provider.PlacementRequest{Subnet: "subnet-x"},
			regions: map[string]region{"eu-north-1": defaultVPC},
			wantErr: provider.ErrNoNetwork, wantErrHas: []string{"no subnet subnet-x"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			at, zone := SplitZone(tc.at)
			in := func(r string) ec2API {
				if reg, ok := tc.regions[r]; ok {
					return reg.mock()
				}
				return region{err: denied}.mock()
			}
			p := &Provider{ec2: in(at), ec2In: in, region: at, zone: zone}
			pl, err := p.ResolvePlacement(context.Background(), tc.req)
			switch {
			case tc.wantPlainErr:
				if err == nil || errors.Is(err, provider.ErrBadRegion) || errors.Is(err, provider.ErrNoNetwork) {
					t.Fatalf("err = %v, want a plain error", err)
				}
				return
			case tc.wantErr != nil:
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				for _, has := range tc.wantErrHas {
					if !strings.Contains(err.Error(), has) {
						t.Errorf("err = %q, want it to contain %q", err, has)
					}
				}
				return
			case err != nil:
				t.Fatalf("ResolvePlacement: %v", err)
			}
			if pl.Region != tc.wantRegion || pl.Subnet != tc.wantSubnet {
				t.Errorf("got region %q subnet %q; want %q %q", pl.Region, pl.Subnet, tc.wantRegion, tc.wantSubnet)
			}
			if p.Region() != tc.wantRegion {
				t.Errorf("the provider works in %q, want %q", p.Region(), tc.wantRegion)
			}
			if len(pl.Notes) != tc.wantNotes {
				t.Errorf("notes = %q, want %d of them", pl.Notes, tc.wantNotes)
			}
		})
	}
}
