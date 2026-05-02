package aws

import (
	"context"
	"errors"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/runlease/runlease/pkg/provider"
)

// mockEC2 implements ec2API. describeOuts are returned sequentially;
// the last entry is repeated once exhausted.
type mockEC2 struct {
	runOut       *ec2.RunInstancesOutput
	runErr       error
	terminateErr error
	describeOuts []*ec2.DescribeInstancesOutput
	describeIdx  int
	describeErr  error
}

func (m *mockEC2) RunInstances(_ context.Context, _ *ec2.RunInstancesInput, _ ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error) {
	return m.runOut, m.runErr
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
