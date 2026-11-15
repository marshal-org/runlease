package aws

import (
	"context"
	"fmt"
	"strings"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
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

// identityRegion is the region to ask STS in: one that every account of the
// partition has enabled, so the answer says something about the credentials
// and nothing about the region a launch is headed for.
func identityRegion(region string) string {
	switch {
	case strings.HasPrefix(region, "us-gov-"):
		return "us-gov-west-1"
	case strings.HasPrefix(region, "cn-"):
		return "cn-north-1"
	}
	return "us-east-1"
}

// AccountName is the AWS account ID, looked up for two seconds at most.
func (p *Provider) AccountName(ctx context.Context) string {
	account, _ := p.caller(ctx)
	return account
}

// Identity is the ARN the credentials belong to.
func (p *Provider) Identity(ctx context.Context) string {
	_, arn := p.caller(ctx)
	return arn
}

func (p *Provider) caller(ctx context.Context) (account, arn string) {
	if p.sts == nil {
		return "", ""
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := p.sts.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return "", ""
	}
	return awssdk.ToString(out.Account), awssdk.ToString(out.Arn)
}

// ConfiguredRegion is the region the user's AWS setup already names, from the
// environment or the active profile. Empty if it names none.
func ConfiguredRegion(ctx context.Context) string {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return ""
	}
	return cfg.Region
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
