package aws

import (
	"context"
	"fmt"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/marshal-org/runlease/pkg/provider"
)

// ListManaged returns every instance in this provider's region that carries
// the runlease-managed tag and still exists. Terminated instances are left
// out: EC2 lists them for a while after they are gone, and they bill nothing.
//
// Disk size and cost are not reported for AWS: they need a call per volume.
func (p *Provider) ListManaged(ctx context.Context) ([]provider.ManagedInstance, error) {
	in := &ec2.DescribeInstancesInput{
		Filters: []types.Filter{
			{Name: awssdk.String("tag:runlease-managed"), Values: []string{"true"}},
			{Name: awssdk.String("instance-state-name"), Values: []string{"pending", "running", "stopping", "stopped"}},
		},
	}
	var out []provider.ManagedInstance
	for {
		page, err := p.ec2.DescribeInstances(ctx, in)
		if err != nil {
			return nil, fmt.Errorf("listing runlease instances in %s: %w", p.region, err)
		}
		for _, r := range page.Reservations {
			for _, inst := range r.Instances {
				out = append(out, p.managedFromInstance(inst))
			}
		}
		if awssdk.ToString(page.NextToken) == "" {
			return out, nil
		}
		in.NextToken = page.NextToken
	}
}

func (p *Provider) managedFromInstance(inst types.Instance) provider.ManagedInstance {
	m := provider.ManagedInstance{
		ID:           provider.InstanceID(awssdk.ToString(inst.InstanceId)),
		Provider:     provider.ProviderAWS,
		Region:       p.region,
		InstanceType: string(inst.InstanceType),
		State:        mapStatus(inst.State),
	}
	if inst.LaunchTime != nil {
		m.CreatedAt = inst.LaunchTime.UTC()
	}
	for _, tag := range inst.Tags {
		switch awssdk.ToString(tag.Key) {
		case "runlease:name":
			m.Name = awssdk.ToString(tag.Value)
		case "runlease-created-by":
			m.CreatedBy = awssdk.ToString(tag.Value)
		case "runlease-expires-at":
			if t, err := time.Parse(time.RFC3339, awssdk.ToString(tag.Value)); err == nil {
				t = t.UTC()
				m.ExpiresAt = &t
			}
		}
	}
	return m
}
