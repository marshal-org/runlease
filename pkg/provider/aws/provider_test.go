package aws

import (
	"context"
	"errors"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/marshal-org/runlease/pkg/provider"
)

// mockEC2 implements ec2API. describeOuts are returned sequentially;
// the last entry is repeated once exhausted.
type mockEC2 struct {
	runOut       *ec2.RunInstancesOutput
	runErr       error
	lastRunInput *ec2.RunInstancesInput
	terminateErr error
	describeOuts []*ec2.DescribeInstancesOutput
	describeIdx  int
	describeErr  error
	imagesOut    *ec2.DescribeImagesOutput
	imagesErr    error
	subnetsOut   *ec2.DescribeSubnetsOutput
	subnetsErr   error
	// routeTables is keyed by the name of the first filter in the request:
	// "association.subnet-id" for a subnet's own table, "vpc-id" for the main one.
	routeTables map[string]*ec2.DescribeRouteTablesOutput
	routesErr   error
}

func (m *mockEC2) DescribeSubnets(_ context.Context, _ *ec2.DescribeSubnetsInput, _ ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error) {
	return m.subnetsOut, m.subnetsErr
}

func (m *mockEC2) DescribeRouteTables(_ context.Context, in *ec2.DescribeRouteTablesInput, _ ...func(*ec2.Options)) (*ec2.DescribeRouteTablesOutput, error) {
	if m.routesErr != nil {
		return nil, m.routesErr
	}
	if out, ok := m.routeTables[awssdk.ToString(in.Filters[0].Name)]; ok {
		return out, nil
	}
	return &ec2.DescribeRouteTablesOutput{}, nil
}

func (m *mockEC2) RunInstances(_ context.Context, in *ec2.RunInstancesInput, _ ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error) {
	m.lastRunInput = in
	return m.runOut, m.runErr
}

func (m *mockEC2) DescribeImages(_ context.Context, _ *ec2.DescribeImagesInput, _ ...func(*ec2.Options)) (*ec2.DescribeImagesOutput, error) {
	return m.imagesOut, m.imagesErr
}

func (m *mockEC2) TerminateInstances(_ context.Context, _ *ec2.TerminateInstancesInput, _ ...func(*ec2.Options)) (*ec2.TerminateInstancesOutput, error) {
	return &ec2.TerminateInstancesOutput{}, m.terminateErr
}

func (m *mockEC2) DescribeInstances(_ context.Context, _ *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	if m.describeErr != nil {
		return nil, m.describeErr
	}
	idx := m.describeIdx
	if idx >= len(m.describeOuts) {
		idx = len(m.describeOuts) - 1
	}
	m.describeIdx++
	return m.describeOuts[idx], nil
}

func newProvider(mock ec2API) *Provider {
	return &Provider{ec2: mock, region: "us-east-1", pollInterval: time.Millisecond}
}

func describeOut(state types.InstanceStateName, publicIP string) *ec2.DescribeInstancesOutput {
	inst := types.Instance{
		InstanceId:      awssdk.String("i-test123"),
		State:           &types.InstanceState{Name: state},
		PublicIpAddress: awssdk.String(publicIP),
	}
	return &ec2.DescribeInstancesOutput{
		Reservations: []types.Reservation{{Instances: []types.Instance{inst}}},
	}
}

func runOut(id string) *ec2.RunInstancesOutput {
	return &ec2.RunInstancesOutput{
		Instances: []types.Instance{{
			InstanceId: awssdk.String(id),
			State:      &types.InstanceState{Name: types.InstanceStateNamePending},
		}},
	}
}

// --- RequestSpot ---

func TestRequestSpot_success(t *testing.T) {
	mock := &mockEC2{runOut: runOut("i-abc")}
	p := newProvider(mock)

	h, err := p.RequestSpot(context.Background(), provider.InstanceSpec{
		ManifestName: "ci-runner",
		InstanceType: "c5.xlarge",
		ImageID:      "ami-123",
		UserData:     "#!/bin/bash\necho hi",
	})
	if err != nil {
		t.Fatalf("RequestSpot: %v", err)
	}
	if h.ID != "i-abc" {
		t.Errorf("ID = %q, want %q", h.ID, "i-abc")
	}
	if h.ManifestName != "ci-runner" {
		t.Errorf("ManifestName = %q, want %q", h.ManifestName, "ci-runner")
	}
	if h.State != provider.StatusPending {
		t.Errorf("State = %q, want pending", h.State)
	}
}

func TestRequestSpot_diskSizeResizesRootVolume(t *testing.T) {
	mock := &mockEC2{
		runOut: runOut("i-abc"),
		imagesOut: &ec2.DescribeImagesOutput{
			Images: []types.Image{{RootDeviceName: awssdk.String("/dev/sda1")}},
		},
	}
	p := newProvider(mock)

	_, err := p.RequestSpot(context.Background(), provider.InstanceSpec{
		InstanceType: "c5.xlarge",
		ImageID:      "ami-123",
		DiskSizeGb:   80,
	})
	if err != nil {
		t.Fatalf("RequestSpot: %v", err)
	}
	bdm := mock.lastRunInput.BlockDeviceMappings
	if len(bdm) != 1 {
		t.Fatalf("BlockDeviceMappings len = %d, want 1", len(bdm))
	}
	if got := awssdk.ToString(bdm[0].DeviceName); got != "/dev/sda1" {
		t.Errorf("DeviceName = %q, want /dev/sda1 (from the AMI's root device)", got)
	}
	if got := awssdk.ToInt32(bdm[0].Ebs.VolumeSize); got != 80 {
		t.Errorf("VolumeSize = %d, want 80", got)
	}
}

func TestRequestSpot_noDiskSizeNoBlockDevice(t *testing.T) {
	mock := &mockEC2{runOut: runOut("i-abc")}
	p := newProvider(mock)
	if _, err := p.RequestSpot(context.Background(), provider.InstanceSpec{InstanceType: "c5.xlarge", ImageID: "ami-123"}); err != nil {
		t.Fatalf("RequestSpot: %v", err)
	}
	if mock.lastRunInput.BlockDeviceMappings != nil {
		t.Errorf("expected no BlockDeviceMappings when DiskSizeGb=0, got %d", len(mock.lastRunInput.BlockDeviceMappings))
	}
}

func TestRequestSpot_apiError(t *testing.T) {
	mock := &mockEC2{runErr: errors.New("insufficient capacity")}
	p := newProvider(mock)

	_, err := p.RequestSpot(context.Background(), provider.InstanceSpec{InstanceType: "c5.xlarge"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// --- Terminate ---

func TestTerminate_success(t *testing.T) {
	p := newProvider(&mockEC2{})
	if err := p.Terminate(context.Background(), "i-abc"); err != nil {
		t.Fatalf("Terminate: %v", err)
	}
}

func TestTerminate_apiError(t *testing.T) {
	mock := &mockEC2{terminateErr: errors.New("not authorized")}
	p := newProvider(mock)
	if err := p.Terminate(context.Background(), "i-abc"); err == nil {
		t.Fatal("expected error, got nil")
	}
}

// --- Status ---

func TestStatus_running(t *testing.T) {
	mock := &mockEC2{
		describeOuts: []*ec2.DescribeInstancesOutput{
			describeOut(types.InstanceStateNameRunning, "1.2.3.4"),
		},
	}
	p := newProvider(mock)
	s, err := p.Status(context.Background(), "i-abc")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if s != provider.StatusRunning {
		t.Errorf("Status = %q, want running", s)
	}
}

func TestStatus_notFound(t *testing.T) {
	mock := &mockEC2{
		describeOuts: []*ec2.DescribeInstancesOutput{{Reservations: nil}},
	}
	p := newProvider(mock)
	_, err := p.Status(context.Background(), "i-missing")
	if err == nil {
		t.Fatal("expected error for missing instance, got nil")
	}
}

// --- WaitUntilRunning ---

func TestWaitUntilRunning_immediatelyRunning(t *testing.T) {
	mock := &mockEC2{
		describeOuts: []*ec2.DescribeInstancesOutput{
			describeOut(types.InstanceStateNameRunning, "5.6.7.8"),
		},
	}
	p := newProvider(mock)
	h, err := p.WaitUntilRunning(context.Background(), "i-abc")
	if err != nil {
		t.Fatalf("WaitUntilRunning: %v", err)
	}
	if h.PublicIP != "5.6.7.8" {
		t.Errorf("PublicIP = %q, want %q", h.PublicIP, "5.6.7.8")
	}
}

func TestWaitUntilRunning_pendingThenRunning(t *testing.T) {
	mock := &mockEC2{
		describeOuts: []*ec2.DescribeInstancesOutput{
			describeOut(types.InstanceStateNamePending, ""),
			describeOut(types.InstanceStateNamePending, ""),
			describeOut(types.InstanceStateNameRunning, "9.9.9.9"),
		},
	}
	p := newProvider(mock)
	h, err := p.WaitUntilRunning(context.Background(), "i-abc")
	if err != nil {
		t.Fatalf("WaitUntilRunning: %v", err)
	}
	if h.State != provider.StatusRunning {
		t.Errorf("State = %q, want running", h.State)
	}
}

func TestWaitUntilRunning_terminatedEarly(t *testing.T) {
	mock := &mockEC2{
		describeOuts: []*ec2.DescribeInstancesOutput{
			describeOut(types.InstanceStateNameTerminated, ""),
		},
	}
	p := newProvider(mock)
	_, err := p.WaitUntilRunning(context.Background(), "i-abc")
	if err == nil {
		t.Fatal("expected error when instance terminates, got nil")
	}
}

func TestWaitUntilRunning_contextCancel(t *testing.T) {
	mock := &mockEC2{
		describeOuts: []*ec2.DescribeInstancesOutput{
			describeOut(types.InstanceStateNamePending, ""),
		},
	}
	p := &Provider{ec2: mock, region: "us-east-1", pollInterval: time.Hour}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := p.WaitUntilRunning(ctx, "i-abc")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected DeadlineExceeded, got %v", err)
	}
}

func TestRequestSpot_requiresIMDSv2(t *testing.T) {
	mock := &mockEC2{runOut: runOut("i-imds")}
	p := newProvider(mock)
	if _, err := p.RequestSpot(context.Background(), provider.InstanceSpec{
		ManifestName: "imds", InstanceType: "t3.small", ImageID: "ami-123", UserData: "#!/bin/bash",
	}); err != nil {
		t.Fatalf("RequestSpot: %v", err)
	}
	mo := mock.lastRunInput.MetadataOptions
	if mo == nil || mo.HttpTokens != types.HttpTokensStateRequired {
		t.Fatalf("instance must require IMDSv2 session tokens, got %+v", mo)
	}
}

// The public address is set on the request, so it does not depend on the
// subnet's auto-assign setting.
func TestRequestSpot_publicIPIsExplicit(t *testing.T) {
	for _, want := range []bool{false, true} {
		mock := &mockEC2{runOut: runOut("i-abc")}
		_, err := newProvider(mock).RequestSpot(context.Background(), provider.InstanceSpec{
			ManifestName:     "ci-runner",
			InstanceType:     "c5.xlarge",
			ImageID:          "ami-123",
			SubnetID:         "subnet-1",
			SecurityGroupIDs: []string{"sg-1"},
			PublicIP:         want,
		})
		if err != nil {
			t.Fatalf("RequestSpot: %v", err)
		}
		in := mock.lastRunInput
		if len(in.NetworkInterfaces) != 1 {
			t.Fatalf("NetworkInterfaces = %d, want 1", len(in.NetworkInterfaces))
		}
		nic := in.NetworkInterfaces[0]
		if got := awssdk.ToBool(nic.AssociatePublicIpAddress); got != want {
			t.Errorf("AssociatePublicIpAddress = %v, want %v", got, want)
		}
		if awssdk.ToString(nic.SubnetId) != "subnet-1" || len(nic.Groups) != 1 || nic.Groups[0] != "sg-1" {
			t.Errorf("subnet and security groups must move to the network interface, got %q %v", awssdk.ToString(nic.SubnetId), nic.Groups)
		}
		// EC2 rejects a request that names them in both places.
		if in.SubnetId != nil || len(in.SecurityGroupIds) != 0 {
			t.Error("subnet / security groups must not also be set at the top level")
		}
	}
}
