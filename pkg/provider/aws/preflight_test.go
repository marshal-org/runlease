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

func TestCheckNetwork(t *testing.T) {
	two := &ec2.DescribeSubnetsOutput{Subnets: []types.Subnet{
		{SubnetId: awssdk.String("subnet-a"), VpcId: awssdk.String("vpc-1")},
		{SubnetId: awssdk.String("subnet-b"), VpcId: awssdk.String("vpc-1")},
	}}
	tests := []struct {
		name      string
		subnet    string
		out       *ec2.DescribeSubnetsOutput
		err       error
		want      string
		noNetwork bool
		wantErr   bool
	}{
		{name: "default VPC", out: two, want: "default VPC vpc-1 (2 subnets)"},
		{name: "named subnet", subnet: "subnet-a", out: two, want: "subnet subnet-a (VPC vpc-1)"},
		{name: "no default VPC", out: &ec2.DescribeSubnetsOutput{}, noNetwork: true, wantErr: true},
		{name: "subnet not found", subnet: "subnet-x",
			err: &smithy.GenericAPIError{Code: "InvalidSubnetID.NotFound", Message: "nope"}, noNetwork: true, wantErr: true},
		{name: "denied is not a missing network",
			err: &smithy.GenericAPIError{Code: "UnauthorizedOperation", Message: "explicit deny in a service control policy"}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &Provider{ec2: &mockEC2{subnetsOut: tc.out, subnetsErr: tc.err}, region: "eu-north-1"}
			got, err := p.CheckNetwork(context.Background(), "", tc.subnet)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if errors.Is(err, provider.ErrNoNetwork) != tc.noNetwork {
				t.Errorf("ErrNoNetwork = %v, want %v (%v)", !tc.noNetwork, tc.noNetwork, err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCheckRegion_keepsTheDenialText(t *testing.T) {
	deny := &smithy.GenericAPIError{Code: "UnauthorizedOperation", Message: "with an explicit deny in a service control policy"}
	p := &Provider{ec2: &mockEC2{subnetsErr: deny}, region: "us-east-1"}
	err := p.CheckRegion(context.Background())
	if err == nil || !strings.Contains(err.Error(), "service control policy") || !strings.Contains(err.Error(), "us-east-1") {
		t.Errorf("err = %v, want the denial and the region", err)
	}
	if err := (&Provider{ec2: &mockEC2{subnetsOut: &ec2.DescribeSubnetsOutput{}}}).CheckRegion(context.Background()); err != nil {
		t.Errorf("a region with no default VPC is still an allowed region: %v", err)
	}
}

// Identity is asked in a region every account has, so a region that is not
// enabled cannot make good credentials look bad.
func TestIdentityRegion(t *testing.T) {
	for region, want := range map[string]string{
		"eu-north-1": "us-east-1", "me-central-1": "us-east-1", "us-east-1": "us-east-1",
		"us-gov-east-1": "us-gov-west-1", "cn-northwest-1": "cn-north-1",
	} {
		if got := identityRegion(region); got != want {
			t.Errorf("identityRegion(%s) = %s, want %s", region, got, want)
		}
	}
}

var _ provider.Preflighter = (*Provider)(nil)
