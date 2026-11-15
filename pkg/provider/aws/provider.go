package aws

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"

	"github.com/marshal-org/runlease/pkg/provider"
)

// ec2API covers the EC2 operations we use, allowing the client to be mocked in tests.
type ec2API interface {
	RunInstances(ctx context.Context, params *ec2.RunInstancesInput, optFns ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error)
	TerminateInstances(ctx context.Context, params *ec2.TerminateInstancesInput, optFns ...func(*ec2.Options)) (*ec2.TerminateInstancesOutput, error)
	DescribeInstances(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
	DescribeImages(ctx context.Context, params *ec2.DescribeImagesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeImagesOutput, error)
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
		// Require IMDSv2. The session-token handshake stops a plain forwarded
		// request (the usual SSRF path) from reading instance credentials or the
		// user-data, which holds the SSH host key and the relay token.
		MetadataOptions: &types.InstanceMetadataOptionsRequest{
			HttpTokens:   types.HttpTokensStateRequired,
			HttpEndpoint: types.InstanceMetadataEndpointStateEnabled,
		},
		TagSpecifications: []types.TagSpecification{
			{ResourceType: types.ResourceTypeInstance, Tags: tags},
		},
	}
	// The public address is decided here, not left to the subnet's
	// auto-assign setting, so `publicIp` in the manifest means the same thing
	// in every VPC. EC2 only takes that choice on a network interface, and
	// then the subnet and security groups have to be given there too.
	nic := types.InstanceNetworkInterfaceSpecification{
		DeviceIndex:              awssdk.Int32(0),
		AssociatePublicIpAddress: awssdk.Bool(spec.PublicIP),
		DeleteOnTermination:      awssdk.Bool(true),
	}
	if len(spec.SecurityGroupIDs) > 0 {
		nic.Groups = spec.SecurityGroupIDs
	}
	if spec.SubnetID != "" {
		nic.SubnetId = awssdk.String(spec.SubnetID)
	}
	input.NetworkInterfaces = []types.InstanceNetworkInterfaceSpecification{nic}
	if spec.InstanceProfileARN != "" {
		input.IamInstanceProfile = &types.IamInstanceProfileSpecification{
			Arn: awssdk.String(spec.InstanceProfileARN),
		}
	}
	if spec.DiskSizeGb > 0 {
		// Resize the AMI's root volume. We look up the actual root device name
		// rather than assuming /dev/xvda, so custom AMIs (e.g. /dev/sda1) get
		// their root resized instead of an extra volume attached.
		input.BlockDeviceMappings = []types.BlockDeviceMapping{{
			DeviceName: awssdk.String(p.rootDeviceName(ctx, spec.ImageID)),
			Ebs:        &types.EbsBlockDevice{VolumeSize: awssdk.Int32(int32(spec.DiskSizeGb))},
		}}
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
		InstanceType: string(inst.InstanceType),
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
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && apiErr.ErrorCode() == "InvalidInstanceID.NotFound" {
			return nil // already gone
		}
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
		ID:           id,
		Provider:     provider.ProviderAWS,
		InstanceType: string(inst.InstanceType),
		Region:       p.region,
		PublicIP:     awssdk.ToString(inst.PublicIpAddress),
		PrivateIP:    awssdk.ToString(inst.PrivateIpAddress),
		State:        mapStatus(inst.State),
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

// rootDeviceName returns the AMI's root device name (e.g. /dev/xvda) so a
// boot-disk resize targets the root volume rather than attaching a second one.
// Falls back to /dev/xvda if the lookup returns nothing.
func (p *Provider) rootDeviceName(ctx context.Context, imageID string) string {
	// Fall back to the common default rather than blocking provisioning: an
	// empty image ID, a DescribeImages denial (restricted IAM), or a missing
	// root device all degrade to /dev/xvda. Genuine image problems still surface
	// from RunInstances.
	if imageID == "" {
		return "/dev/xvda"
	}
	out, err := p.ec2.DescribeImages(ctx, &ec2.DescribeImagesInput{ImageIds: []string{imageID}})
	if err != nil || len(out.Images) == 0 || awssdk.ToString(out.Images[0].RootDeviceName) == "" {
		return "/dev/xvda"
	}
	return awssdk.ToString(out.Images[0].RootDeviceName)
}

// ResolveAL2023AMI returns the latest Amazon Linux 2023 x86_64 AMI ID for the
// given region by querying DescribeImages. No special IAM permissions required
// beyond the standard EC2 read access already needed for provisioning.
func ResolveAL2023AMI(ctx context.Context, region string) (string, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return "", fmt.Errorf("loading AWS config: %w", err)
	}
	client := ec2.NewFromConfig(cfg)
	out, err := client.DescribeImages(ctx, &ec2.DescribeImagesInput{
		Owners: []string{"amazon"},
		Filters: []types.Filter{
			{Name: awssdk.String("name"), Values: []string{"al2023-ami-*-x86_64"}},
			{Name: awssdk.String("state"), Values: []string{"available"}},
			{Name: awssdk.String("architecture"), Values: []string{"x86_64"}},
			{Name: awssdk.String("virtualization-type"), Values: []string{"hvm"}},
		},
	})
	if err != nil {
		return "", fmt.Errorf("DescribeImages: %w", err)
	}
	if len(out.Images) == 0 {
		return "", fmt.Errorf("no Amazon Linux 2023 AMI found in %s", region)
	}
	sort.Slice(out.Images, func(i, j int) bool {
		return awssdk.ToString(out.Images[i].CreationDate) > awssdk.ToString(out.Images[j].CreationDate)
	})
	return awssdk.ToString(out.Images[0].ImageId), nil
}

func buildTags(name string, extra map[string]string) []types.Tag {
	tags := []types.Tag{
		// Name appears as the display name in the AWS console.
		{Key: awssdk.String("Name"), Value: awssdk.String(name)},
		{Key: awssdk.String("runlease:name"), Value: awssdk.String(name)},
	}
	for k, v := range extra {
		tags = append(tags, types.Tag{Key: awssdk.String(k), Value: awssdk.String(v)})
	}
	return tags
}
