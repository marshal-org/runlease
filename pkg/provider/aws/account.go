package aws

import (
	"context"
	"fmt"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

type stsAPI interface {
	GetCallerIdentity(ctx context.Context, params *sts.GetCallerIdentityInput, optFns ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error)
}

// DescribeAccount says which account, region and identity this provider acts
// with. The lookup is given two seconds and left out if it fails.
func (p *Provider) DescribeAccount(ctx context.Context) string {
	s := "AWS region " + p.region
	if p.sts == nil {
		return s
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := p.sts.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return s
	}
	return fmt.Sprintf("AWS account %s, region %s, as %s", awssdk.ToString(out.Account), p.region, awssdk.ToString(out.Arn))
}

// NATAdvice explains the AWS way out. There is no two-line command for it: a
// NAT gateway needs a public subnet to live in and a route pointing at it.
func (p *Provider) NATAdvice(_, subnet string) string {
	which := "the subnet"
	if subnet != "" {
		which = "subnet " + subnet
	}
	return fmt.Sprintf("In the VPC console: create a NAT gateway in a public subnet, then add a route\n"+
		"0.0.0.0/0 -> that NAT gateway to the route table of %s.\n"+
		"(A NAT gateway is billed by AWS by the hour and by traffic. Subnets of the default VPC are\n"+
		" public and have none, so --public-ip is the usual choice there. If you may not change this\n"+
		" VPC, launch in a subnet that has a NAT gateway with --subnet SUBNET_ID.)", which)
}
