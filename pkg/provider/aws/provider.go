package aws

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/runlease/runlease/pkg/provider"
)

// ec2API covers the EC2 operations we use, allowing the client to be mocked in tests.
type ec2API interface {
	RunInstances(ctx context.Context, params *ec2.RunInstancesInput, optFns ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error)
	TerminateInstances(ctx context.Context, params *ec2.TerminateInstancesInput, optFns ...func(*ec2.Options)) (*ec2.TerminateInstancesOutput, error)
	DescribeInstances(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
}

type Provider struct {
	ec2          ec2API
	region       string
	pollInterval time.Duration
}

func New(ctx context.Context, region string) (*Provider, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	return &Provider{
		ec2:          ec2.NewFromConfig(cfg),
		region:       region,
		pollInterval: 5 * time.Second,
	}, nil
}

func (p *Provider) RequestSpot(ctx context.Context, spec provider.InstanceSpec) (*provider.InstanceHandle, error) {
	encoded := base64.StdEncoding.EncodeToString([]byte(spec.UserData))

	tags := buildTags(spec.ManifestName, spec.Tags)
	input := &ec2.RunInstancesInput{
		ImageId:      awssdk.String(spec.ImageID),
		InstanceType: types.InstanceType(spec.InstanceType),
		MinCount:     awssdk.Int32(1),
		MaxCount:     awssdk.Int32(1),
		UserData:     awssdk.String(encoded),
		InstanceMarketOptions: &types.InstanceMarketOptionsRequest{
			MarketType: types.MarketTypeSpot,
		},
		// Terminate (not stop) when the OS shuts down, so the TTL-triggered
		// shutdown -h in the startup script terminates the instance.
		InstanceInitiatedShutdownBehavior: types.ShutdownBehaviorTerminate,
		TagSpecifications: []types.TagSpecification{
			{ResourceType: types.ResourceTypeInstance, Tags: tags},
		},
	}
	if len(spec.SecurityGroupIDs) > 0 {
		input.SecurityGroupIds = spec.SecurityGroupIDs
	}
	if spec.SubnetID != "" {
		input.SubnetId = awssdk.String(spec.SubnetID)
	}
	if spec.InstanceProfileARN != "" {
		input.IamInstanceProfile = &types.IamInstanceProfileSpecification{
			Arn: awssdk.String(spec.InstanceProfileARN),
		}
	}

	out, err := p.ec2.RunInstances(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("RunInstances: %w", err)
	}
	if len(out.Instances) == 0 {
		return nil, fmt.Errorf("RunInstances returned no instances")
	}

	inst := out.Instances[0]
	return &provider.InstanceHandle{
		ID:           provider.InstanceID(awssdk.ToString(inst.InstanceId)),
		Provider:     provider.ProviderAWS,
		Region:       p.region,
		State:        mapStatus(inst.State),
		ManifestName: spec.ManifestName,
		CreatedAt:    time.Now().UTC(),
		Tags:         spec.Tags,
	}, nil
}

func (p *Provider) Terminate(ctx context.Context, id provider.InstanceID) error {
	_, err := p.ec2.TerminateInstances(ctx, &ec2.TerminateInstancesInput{
		InstanceIds: []string{string(id)},
	})
	if err != nil {
		return fmt.Errorf("TerminateInstances: %w", err)
	}
	return nil
}

func (p *Provider) Status(ctx context.Context, id provider.InstanceID) (provider.InstanceStatus, error) {
	h, err := p.describe(ctx, id)
	if err != nil {
		return provider.StatusUnknown, err
	}
	return h.State, nil
}

func (p *Provider) WaitUntilRunning(ctx context.Context, id provider.InstanceID) (*provider.InstanceHandle, error) {
	ticker := time.NewTicker(p.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			h, err := p.describe(ctx, id)
			if err != nil {
				return nil, err
			}
			switch h.State {
			case provider.StatusRunning:
				return h, nil
			case provider.StatusTerminated, provider.StatusStopped:
				return nil, fmt.Errorf("instance %s entered state %s before running", id, h.State)
			}
		}
	}
}

func (p *Provider) describe(ctx context.Context, id provider.InstanceID) (*provider.InstanceHandle, error) {
	out, err := p.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		InstanceIds: []string{string(id)},
	})
	if err != nil {
		return nil, fmt.Errorf("DescribeInstances: %w", err)
	}
	if len(out.Reservations) == 0 || len(out.Reservations[0].Instances) == 0 {
		return nil, fmt.Errorf("instance %s not found", id)
	}
	inst := out.Reservations[0].Instances[0]
	return &provider.InstanceHandle{
		ID:        id,
		Provider:  provider.ProviderAWS,
		Region:    p.region,
		PublicIP:  awssdk.ToString(inst.PublicIpAddress),
		PrivateIP: awssdk.ToString(inst.PrivateIpAddress),
		State:     mapStatus(inst.State),
	}, nil
}

func mapStatus(state *types.InstanceState) provider.InstanceStatus {
	if state == nil {
		return provider.StatusUnknown
	}
	switch state.Name {
	case types.InstanceStateNamePending:
		return provider.StatusPending
	case types.InstanceStateNameRunning:
		return provider.StatusRunning
	case types.InstanceStateNameStopped, types.InstanceStateNameStopping:
		return provider.StatusStopped
	case types.InstanceStateNameTerminated, types.InstanceStateNameShuttingDown:
		return provider.StatusTerminated
	default:
		return provider.StatusUnknown
	}
}

func buildTags(name string, extra map[string]string) []types.Tag {
	tags := []types.Tag{
		{Key: awssdk.String("runlease:name"), Value: awssdk.String(name)},
	}
	for k, v := range extra {
		tags = append(tags, types.Tag{Key: awssdk.String(k), Value: awssdk.String(v)})
	}
	return tags
}
