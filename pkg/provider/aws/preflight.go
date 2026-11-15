package aws

import (
	"context"
	"errors"
	"fmt"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"

	"github.com/marshal-org/runlease/pkg/provider"
)

// CheckAccess asks STS who the caller is. AWS answers that for any valid
// credentials, whatever their permissions and whatever the region allows, so
// a failure here is about the sign-in and nothing else.
func (p *Provider) CheckAccess(ctx context.Context) error {
	if p.sts == nil {
		return nil
	}
	if _, err := p.sts.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{}); err != nil {
		return fmt.Errorf("GetCallerIdentity: %w", err)
	}
	return nil
}

// CheckRegion makes one read-only EC2 call in the region. An organization
// that does not allow the region denies it, as it would deny a launch.
func (p *Provider) CheckRegion(ctx context.Context) error {
	if _, err := p.ec2.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{Filters: defaultSubnetFilter()}); err != nil {
		return fmt.Errorf("DescribeSubnets in %s: %w", p.region, err)
	}
	return nil
}

// CheckNetwork reads the named subnet, or the default VPC's subnets when none
// is named: that is where EC2 places an instance launched without one.
func (p *Provider) CheckNetwork(ctx context.Context, _, subnet string) (string, error) {
	in := &ec2.DescribeSubnetsInput{Filters: defaultSubnetFilter()}
	if subnet != "" {
		in = &ec2.DescribeSubnetsInput{SubnetIds: []string{subnet}}
	}
	out, err := p.ec2.DescribeSubnets(ctx, in)
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && apiErr.ErrorCode() == "InvalidSubnetID.NotFound" {
			return "", fmt.Errorf("%w: %s has no subnet %s", provider.ErrNoNetwork, p.region, subnet)
		}
		return "", fmt.Errorf("DescribeSubnets: %w", err)
	}
	if len(out.Subnets) == 0 {
		if subnet != "" {
			return "", fmt.Errorf("%w: %s has no subnet %s", provider.ErrNoNetwork, p.region, subnet)
		}
		return "", fmt.Errorf("%w: %s has no default VPC", provider.ErrNoNetwork, p.region)
	}
	vpc := awssdk.ToString(out.Subnets[0].VpcId)
	if subnet != "" {
		return fmt.Sprintf("subnet %s (VPC %s)", subnet, vpc), nil
	}
	return fmt.Sprintf("default VPC %s (%d subnets)", vpc, len(out.Subnets)), nil
}

func defaultSubnetFilter() []types.Filter {
	return []types.Filter{{Name: awssdk.String("default-for-az"), Values: []string{"true"}}}
}
