package provider

import (
	"context"
	"time"
)

type ProviderType string
type InstanceID string
type InstanceStatus string

const (
	ProviderAWS   ProviderType = "aws"
	ProviderGCP   ProviderType = "gcp"
	ProviderAzure ProviderType = "azure"

	StatusPending    InstanceStatus = "pending"
	StatusRunning    InstanceStatus = "running"
	StatusStopped    InstanceStatus = "stopped"
	StatusTerminated InstanceStatus = "terminated"
	StatusUnknown    InstanceStatus = "unknown"
)

type InstanceSpec struct {
	ManifestName       string
	InstanceType       string
	Region             string
	ImageID            string
	SecurityGroupIDs   []string          // AWS: security group IDs
	SubnetID           string            // AWS: subnet ID; GCP: subnetwork self-link
	UserData           string            // raw script; provider is responsible for encoding
	Tags               map[string]string // AWS: instance tags; GCP: labels
	InstanceProfileARN string            // AWS only: IAM instance profile (enables SSM)
	ServiceAccountEmail string           // GCP only: service account email (enables IAP etc.)
	NetworkTags        []string          // GCP only: network tags for firewall rule matching
	PublicIP           bool              // assign a public IP; default false (relay mode) or true (direct SSH)
	SelfTermination    bool              // install runlease-terminate and grant compute-delete scope (GCP)
}

type InstanceHandle struct {
	ID           InstanceID
	Provider     ProviderType
	Region       string
	PublicIP     string
	PrivateIP    string
	State        InstanceStatus
	ManifestName string
	CreatedAt    time.Time
	ExpiresAt    *time.Time
	Tags         map[string]string
}

// CloudProvider is the M0 interface. Additional methods (ProvisionResources,
// ResolveInstanceType, InterruptionPending, etc.) will be added in M1.
type CloudProvider interface {
	RequestSpot(ctx context.Context, spec InstanceSpec) (*InstanceHandle, error)
	Terminate(ctx context.Context, id InstanceID) error
	Status(ctx context.Context, id InstanceID) (InstanceStatus, error)
	WaitUntilRunning(ctx context.Context, id InstanceID) (*InstanceHandle, error)
}
