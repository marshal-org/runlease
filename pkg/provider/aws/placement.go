package aws

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"

	"github.com/marshal-org/runlease/pkg/provider"
)

var zoneName = regexp.MustCompile(`^([a-z]{2}(?:-[a-z]+)+-[0-9]+)[a-z]$`)

// SplitZone separates an availability zone (eu-north-1a) into its region and
// itself. A region comes back unchanged, with no zone.
func SplitZone(location string) (region, zone string) {
	if m := zoneName.FindStringSubmatch(location); m != nil {
		return m[1], location
	}
	return location, ""
}

// ResolvePlacement fills in what a launch needs and the user did not name:
//
//   - an availability zone given as the region becomes the region, and the
//     launch goes to the subnet in that zone;
//   - a region the account may not use (an organization policy, a region not
//     enabled) is replaced by the one region it may use, when the region was
//     not the user's choice for this launch;
//   - in a region with no default VPC, a subnet is picked from the one VPC
//     that has a route to the internet of the kind the machine needs.
//
// When there is more than one candidate it does not guess: the error lists
// them. Every call it makes is read-only.
func (p *Provider) ResolvePlacement(ctx context.Context, req provider.PlacementRequest) (*provider.Placement, error) {
	pl := &provider.Placement{Subnet: req.Subnet}

	subnets, err := p.launchSubnets(ctx, req.Subnet)
	if err != nil && isNoSuchHost(err) {
		return nil, fmt.Errorf("%w: AWS has no region %s (regions look like us-east-1), or there is no network connection",
			provider.ErrBadRegion, p.region)
	}
	if err != nil && isDenied(err) {
		// Denied here does not say why. Asking the other regions does: if
		// some answer, it is this region the account may not use.
		usable := p.usableRegions(ctx)
		switch {
		case len(usable) == 0:
			return nil, err
		case len(usable) == 1 && req.AnyRegion && req.Subnet == "" && p.zone == "":
			pl.Notes = append(pl.Notes, fmt.Sprintf("region %s (the only one this AWS account may use; %s is blocked)", usable[0], p.region))
			p.region, p.ec2 = usable[0], p.ec2In(usable[0])
			if subnets, err = p.launchSubnets(ctx, ""); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("%w: this AWS account may not use %s. It may use: %s\nPick one with --region REGION (or save it: runlease configure)",
				provider.ErrBadRegion, p.region, strings.Join(usable, ", "))
		}
	}
	if err != nil {
		return nil, err
	}
	pl.Region = p.region

	switch {
	case req.Subnet != "":
		// Named and found: nothing to choose.
	case len(subnets) > 0 && p.zone == "":
		// A default VPC: EC2 places the machine in one of its subnets.
	case len(subnets) > 0:
		sn := inZone(subnets, p.zone)
		if sn == nil {
			return nil, fmt.Errorf("%w: the default VPC of %s has no subnet in %s. It has subnets in: %s",
				provider.ErrNoNetwork, p.region, p.zone, strings.Join(zonesOf(subnets), ", "))
		}
		pl.Subnet = awssdk.ToString(sn.SubnetId)
		pl.Notes = append(pl.Notes, fmt.Sprintf("subnet %s (the default VPC's subnet in %s)", pl.Subnet, p.zone))
	default:
		sn, why, err := p.chooseSubnet(ctx, req.PublicIP)
		if err != nil {
			return nil, err
		}
		pl.Subnet = awssdk.ToString(sn.SubnetId)
		pl.Notes = append(pl.Notes, fmt.Sprintf("subnet %s in %s (%s has no default VPC; %s)",
			pl.Subnet, awssdk.ToString(sn.AvailabilityZone), p.region, why))
	}
	return pl, nil
}

// launchSubnets returns the named subnet, or with none named the default
// VPC's subnets: that is where EC2 places an instance launched without one.
func (p *Provider) launchSubnets(ctx context.Context, subnet string) ([]types.Subnet, error) {
	in := &ec2.DescribeSubnetsInput{Filters: defaultSubnetFilter()}
	if subnet != "" {
		in = &ec2.DescribeSubnetsInput{SubnetIds: []string{subnet}}
	}
	out, err := p.ec2.DescribeSubnets(ctx, in)
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) && apiErr.ErrorCode() == "InvalidSubnetID.NotFound" || err == nil && subnet != "" && len(out.Subnets) == 0 {
		return nil, fmt.Errorf("%w: %s has no subnet %s", provider.ErrNoNetwork, p.region, subnet)
	}
	if err != nil {
		return nil, fmt.Errorf("DescribeSubnets in %s: %w", p.region, err)
	}
	return out.Subnets, nil
}

// chooseSubnet picks a subnet in a region that has no default VPC. A machine
// with a public IP needs a subnet whose default route is an internet gateway;
// one without needs a NAT gateway or the like. If no subnet has the kind
// asked for, one of the other kind is still returned: the check that follows
// in `up` then says what to change. Subnets with no way out are never picked.
func (p *Provider) chooseSubnet(ctx context.Context, publicIP bool) (*types.Subnet, string, error) {
	out, err := p.ec2.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{})
	if err != nil {
		return nil, "", fmt.Errorf("DescribeSubnets in %s: %w", p.region, err)
	}
	createDefault := fmt.Sprintf("Create the default VPC:\n  aws ec2 create-default-vpc --region %s\nor launch in a subnet of your own with --subnet SUBNET_ID", p.region)
	if len(out.Subnets) == 0 {
		return nil, "", fmt.Errorf("%w: %s has no VPC to launch in. %s", provider.ErrNoNetwork, p.region, createDefault)
	}
	tables, err := p.ec2.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{})
	if err != nil {
		return nil, "", fmt.Errorf("DescribeRouteTables in %s: %w", p.region, err)
	}
	routes := routesBySubnet(out.Subnets, tables.RouteTables)

	var public, private []types.Subnet
	for _, sn := range out.Subnets {
		switch r := routes[awssdk.ToString(sn.SubnetId)]; {
		case routesHaveInternetGateway(r):
			public = append(public, sn)
		case routesHavePrivateEgress(r):
			private = append(private, sn)
		}
	}
	want, kind := private, "it reaches the internet through a NAT gateway"
	if publicIP || len(private) == 0 {
		want, kind = public, "it has an internet gateway"
	}
	if len(want) == 0 {
		want, kind = private, "it reaches the internet through a NAT gateway"
	}
	if len(want) == 0 {
		return nil, "", fmt.Errorf("%w: none of the %d subnets in %s has a route to the internet. %s",
			provider.ErrNoNetwork, len(out.Subnets), p.region, createDefault)
	}
	sort.Slice(want, func(i, j int) bool {
		return awssdk.ToString(want[i].AvailabilityZone)+awssdk.ToString(want[i].SubnetId) <
			awssdk.ToString(want[j].AvailabilityZone)+awssdk.ToString(want[j].SubnetId)
	})
	if p.zone != "" {
		var there []types.Subnet
		for _, sn := range want {
			if awssdk.ToString(sn.AvailabilityZone) == p.zone {
				there = append(there, sn)
			}
		}
		if len(there) == 0 {
			return nil, "", fmt.Errorf("%w: %s has no usable subnet in %s. It has them in: %s",
				provider.ErrNoNetwork, p.region, p.zone, strings.Join(zonesOf(want), ", "))
		}
		want = there
	}
	vpcs := map[string]bool{}
	for _, sn := range want {
		vpcs[awssdk.ToString(sn.VpcId)] = true
	}
	if len(vpcs) > 1 {
		lines := make([]string, 0, len(want))
		for _, sn := range want {
			lines = append(lines, fmt.Sprintf("  --subnet %s   (VPC %s, %s)",
				awssdk.ToString(sn.SubnetId), awssdk.ToString(sn.VpcId), awssdk.ToString(sn.AvailabilityZone)))
		}
		return nil, "", fmt.Errorf("%w: %s has no default VPC, and %d VPCs a machine could join, so name a subnet:\n%s\n(in a manifest: aws.subnetID)",
			provider.ErrNoNetwork, p.region, len(vpcs), strings.Join(lines, "\n"))
	}
	return &want[0], kind, nil
}

// routesBySubnet gives each subnet the routes it uses: those of the route
// table it is associated with, or of its VPC's main table when it has none.
func routesBySubnet(subnets []types.Subnet, tables []types.RouteTable) map[string][]types.Route {
	own, main := map[string][]types.Route{}, map[string][]types.Route{}
	for _, rt := range tables {
		for _, a := range rt.Associations {
			if id := awssdk.ToString(a.SubnetId); id != "" {
				own[id] = rt.Routes
			}
			if awssdk.ToBool(a.Main) {
				main[awssdk.ToString(rt.VpcId)] = rt.Routes
			}
		}
	}
	out := map[string][]types.Route{}
	for _, sn := range subnets {
		id := awssdk.ToString(sn.SubnetId)
		if r, ok := own[id]; ok {
			out[id] = r
		} else {
			out[id] = main[awssdk.ToString(sn.VpcId)]
		}
	}
	return out
}

// routesHaveInternetGateway reports whether an active IPv4 default route
// leaves through an internet gateway, which a machine with a public IP uses.
func routesHaveInternetGateway(routes []types.Route) bool {
	for _, r := range routes {
		if awssdk.ToString(r.DestinationCidrBlock) == "0.0.0.0/0" && r.State == types.RouteStateActive &&
			strings.HasPrefix(awssdk.ToString(r.GatewayId), "igw-") {
			return true
		}
	}
	return false
}

// probeRegions are the regions every commercial AWS account has enabled
// without opting in. They are what usableRegions asks when the region a
// launch was headed for refuses it.
var probeRegions = []string{
	"us-east-1", "us-east-2", "us-west-1", "us-west-2", "ca-central-1", "sa-east-1",
	"eu-west-1", "eu-west-2", "eu-west-3", "eu-central-1", "eu-north-1",
	"ap-south-1", "ap-northeast-1", "ap-northeast-2", "ap-northeast-3", "ap-southeast-1", "ap-southeast-2",
}

// usableRegions asks each of probeRegions, at once, the one read-only
// question a launch starts with, and returns those that answer. It is empty
// for the other partitions (GovCloud, China), which it does not know.
func (p *Provider) usableRegions(ctx context.Context) []string {
	if p.ec2In == nil || identityRegion(p.region) != "us-east-1" {
		return nil
	}
	var (
		mu     sync.Mutex
		wg     sync.WaitGroup
		usable []string
	)
	for _, r := range probeRegions {
		if r == p.region {
			continue
		}
		wg.Add(1)
		go func(r string) {
			defer wg.Done()
			_, err := p.ec2In(r).DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{Filters: defaultSubnetFilter()})
			if err == nil {
				mu.Lock()
				usable = append(usable, r)
				mu.Unlock()
			}
		}(r)
	}
	wg.Wait()
	sort.Strings(usable)
	return usable
}

// isDenied reports whether AWS refused the call itself, as it does for a
// region an organization does not allow or the account has not enabled.
func isDenied(err error) bool {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.ErrorCode() {
	case "UnauthorizedOperation", "AuthFailure", "OptInRequired", "AccessDenied", "AccessDeniedException":
		return true
	}
	return false
}

// isNoSuchHost reports whether the region's endpoint does not resolve, which
// is how a region name that does not exist shows up.
func isNoSuchHost(err error) bool {
	return strings.Contains(err.Error(), "no such host")
}

func inZone(subnets []types.Subnet, zone string) *types.Subnet {
	for i := range subnets {
		if awssdk.ToString(subnets[i].AvailabilityZone) == zone {
			return &subnets[i]
		}
	}
	return nil
}

func zonesOf(subnets []types.Subnet) []string {
	seen := map[string]bool{}
	var zones []string
	for _, sn := range subnets {
		if z := awssdk.ToString(sn.AvailabilityZone); !seen[z] {
			seen[z] = true
			zones = append(zones, z)
		}
	}
	sort.Strings(zones)
	return zones
}
