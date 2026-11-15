package aws

import (
	"context"
	"fmt"
	"strings"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// HasPrivateEgress reports whether an instance with no public IP in the subnet
// would have a route to the internet. It is what lets `up` refuse, before
// creating anything, a machine that could never dial the relay.
//
// The answer comes from the subnet's route table: a default route through a
// NAT gateway, a transit gateway, a NAT instance or a peering connection
// counts. A default route through an internet gateway does not, because an
// instance needs a public IP to use one. With no subnet given, the default
// VPC's subnets are checked, since that is where EC2 would place the instance.
//
// network is unused: on AWS the subnet identifies its VPC.
func (p *Provider) HasPrivateEgress(ctx context.Context, _, subnet string) (bool, error) {
	in := &ec2.DescribeSubnetsInput{}
	if subnet != "" {
		in.SubnetIds = []string{subnet}
	} else {
		in.Filters = []types.Filter{{Name: awssdk.String("default-for-az"), Values: []string{"true"}}}
	}
	subnets, err := p.ec2.DescribeSubnets(ctx, in)
	if err != nil {
		return false, fmt.Errorf("DescribeSubnets: %w", err)
	}
	if len(subnets.Subnets) == 0 {
		return false, fmt.Errorf("no subnet to check: none given, and this region has no default VPC")
	}

	// EC2 picks one of the default subnets, and we cannot know which. Report
	// egress only if every one of them has it.
	for _, sn := range subnets.Subnets {
		ok, err := p.subnetHasPrivateEgress(ctx, awssdk.ToString(sn.SubnetId), awssdk.ToString(sn.VpcId))
		if err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

func (p *Provider) subnetHasPrivateEgress(ctx context.Context, subnetID, vpcID string) (bool, error) {
	// A subnet uses the route table it is associated with, or the VPC's main
	// table when it has no association of its own.
	out, err := p.ec2.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{
		Filters: []types.Filter{{Name: awssdk.String("association.subnet-id"), Values: []string{subnetID}}},
	})
	if err != nil {
		return false, fmt.Errorf("DescribeRouteTables: %w", err)
	}
	if len(out.RouteTables) == 0 {
		out, err = p.ec2.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{
			Filters: []types.Filter{
				{Name: awssdk.String("vpc-id"), Values: []string{vpcID}},
				{Name: awssdk.String("association.main"), Values: []string{"true"}},
			},
		})
		if err != nil {
			return false, fmt.Errorf("DescribeRouteTables: %w", err)
		}
	}
	for _, rt := range out.RouteTables {
		if routesHavePrivateEgress(rt.Routes) {
			return true, nil
		}
	}
	return false, nil
}

// routesHavePrivateEgress reports whether an active IPv4 default route leaves
// through something an instance with no public IP can use.
func routesHavePrivateEgress(routes []types.Route) bool {
	for _, r := range routes {
		if awssdk.ToString(r.DestinationCidrBlock) != "0.0.0.0/0" || r.State != types.RouteStateActive {
			continue
		}
		if awssdk.ToString(r.NatGatewayId) != "" ||
			awssdk.ToString(r.TransitGatewayId) != "" ||
			awssdk.ToString(r.InstanceId) != "" ||
			awssdk.ToString(r.NetworkInterfaceId) != "" ||
			awssdk.ToString(r.VpcPeeringConnectionId) != "" {
			return true
		}
		// What is left is a gateway route. An internet gateway (igw-) needs a
		// public IP on the instance; a virtual private gateway (vgw-) does not.
		if gw := awssdk.ToString(r.GatewayId); gw != "" && !strings.HasPrefix(gw, "igw-") {
			return true
		}
	}
	return false
}
