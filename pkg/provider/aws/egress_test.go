package aws

import (
	"context"
	"errors"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

func defaultRoute(set func(*types.Route)) types.Route {
	r := types.Route{DestinationCidrBlock: awssdk.String("0.0.0.0/0"), State: types.RouteStateActive}
	set(&r)
	return r
}

func TestRoutesHavePrivateEgress(t *testing.T) {
	igw := defaultRoute(func(r *types.Route) { r.GatewayId = awssdk.String("igw-1") })
	nat := defaultRoute(func(r *types.Route) { r.NatGatewayId = awssdk.String("nat-1") })
	tgw := defaultRoute(func(r *types.Route) { r.TransitGatewayId = awssdk.String("tgw-1") })
	vgw := defaultRoute(func(r *types.Route) { r.GatewayId = awssdk.String("vgw-1") })
	blackhole := defaultRoute(func(r *types.Route) {
		r.NatGatewayId = awssdk.String("nat-gone")
		r.State = types.RouteStateBlackhole
	})
	local := types.Route{DestinationCidrBlock: awssdk.String("10.0.0.0/16"), GatewayId: awssdk.String("local"), State: types.RouteStateActive}

	tests := []struct {
		name   string
		routes []types.Route
		want   bool
	}{
		{"no routes", nil, false},
		{"local only", []types.Route{local}, false},
		{"internet gateway needs a public IP", []types.Route{local, igw}, false},
		{"NAT gateway", []types.Route{local, nat}, true},
		{"transit gateway", []types.Route{tgw}, true},
		{"virtual private gateway", []types.Route{vgw}, true},
		{"NAT gateway route that is a blackhole", []types.Route{blackhole}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := routesHavePrivateEgress(tc.routes); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestHasPrivateEgress(t *testing.T) {
	subnet := func(id string) types.Subnet {
		return types.Subnet{SubnetId: awssdk.String(id), VpcId: awssdk.String("vpc-1")}
	}
	table := func(routes ...types.Route) *ec2.DescribeRouteTablesOutput {
		return &ec2.DescribeRouteTablesOutput{RouteTables: []types.RouteTable{{Routes: routes}}}
	}
	igw := defaultRoute(func(r *types.Route) { r.GatewayId = awssdk.String("igw-1") })
	nat := defaultRoute(func(r *types.Route) { r.NatGatewayId = awssdk.String("nat-1") })

	tests := []struct {
		name    string
		mock    *mockEC2
		subnet  string
		want    bool
		wantErr bool
	}{
		{
			name:   "subnet with its own table through a NAT gateway",
			mock:   &mockEC2{subnetsOut: &ec2.DescribeSubnetsOutput{Subnets: []types.Subnet{subnet("subnet-1")}}, routeTables: map[string]*ec2.DescribeRouteTablesOutput{"association.subnet-id": table(nat)}},
			subnet: "subnet-1",
			want:   true,
		},
		{
			name: "default VPC: main table through an internet gateway",
			mock: &mockEC2{subnetsOut: &ec2.DescribeSubnetsOutput{Subnets: []types.Subnet{subnet("subnet-a"), subnet("subnet-b")}}, routeTables: map[string]*ec2.DescribeRouteTablesOutput{"vpc-id": table(igw)}},
		},
		{
			name:   "falls back to the main table when the subnet has none of its own",
			mock:   &mockEC2{subnetsOut: &ec2.DescribeSubnetsOutput{Subnets: []types.Subnet{subnet("subnet-1")}}, routeTables: map[string]*ec2.DescribeRouteTablesOutput{"vpc-id": table(nat)}},
			subnet: "subnet-1",
			want:   true,
		},
		{
			name:    "no default VPC and no subnet given: cannot check",
			mock:    &mockEC2{subnetsOut: &ec2.DescribeSubnetsOutput{}},
			wantErr: true,
		},
		{
			name:    "no permission to read route tables: cannot check",
			mock:    &mockEC2{subnetsOut: &ec2.DescribeSubnetsOutput{Subnets: []types.Subnet{subnet("subnet-1")}}, routesErr: errors.New("UnauthorizedOperation")},
			subnet:  "subnet-1",
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := newProvider(tc.mock).HasPrivateEgress(context.Background(), "", tc.subnet)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
