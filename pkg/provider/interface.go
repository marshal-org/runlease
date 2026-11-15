package provider

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned (wrapped) by Terminate when the instance does not
// exist in the location the provider looked in. It is NOT proof the instance
// was deleted: a provider pointed at the wrong project/region also gets it.
var ErrNotFound = errors.New("instance not found")

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

// A note on RequestSpot: it may return a handle together with an error. That
// means the create request was accepted but did not finish cleanly, so the
// instance exists or may come to exist. Callers must record or terminate it.
type InstanceSpec struct {
	ManifestName        string
	InstanceType        string
	Region              string
	ImageID             string
	SecurityGroupIDs    []string          // AWS: security group IDs
	SubnetID            string            // AWS: subnet ID; GCP: subnetwork name or self-link
	NetworkID           string            // GCP only: VPC network name or self-link (subnetwork alone usually suffices)
	UserData            string            // raw script; provider is responsible for encoding
	Tags                map[string]string // AWS: instance tags; GCP: labels
	InstanceProfileARN  string            // AWS only: IAM instance profile (enables SSM)
	ServiceAccountEmail string            // GCP only: service account email (enables IAP etc.)
	NetworkTags         []string          // GCP only: network tags for firewall rule matching
	PublicIP            bool              // assign a public IP; default false (relay mode) or true (direct SSH)
	SelfTermination     bool              // install runlease-terminate and grant compute-delete scope (GCP)
	DiskSizeGb          int               // boot disk size in GB; 0 = use the image's default
}

type InstanceHandle struct {
	ID           InstanceID
	Provider     ProviderType
	InstanceType string
	Region       string
	Project      string // GCP only: project the instance was created in
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

// ProjectUnknown reports a GCP handle written before runlease recorded the
// project. Its commands use the env / gcloud project, so a 404 may only mean
// "wrong project": never treat it as proof the VM is gone.
func (h InstanceHandle) ProjectUnknown() bool {
	return h.Provider == ProviderGCP && h.Project == ""
}

// EgressChecker is implemented by providers that can tell, before anything is
// created, whether an instance with no public IP would have a route out.
type EgressChecker interface {
	// HasPrivateEgress reports whether the subnet (or, with none given, the
	// one the provider would pick) has a NAT gateway for instances with no
	// public IP. network is GCP only. An error means the check could not be
	// made, not that there is no NAT.
	HasPrivateEgress(ctx context.Context, network, subnet string) (bool, error)
}

// AccountDescriber is implemented by providers that can say, in one line,
// which project or account and which identity a launch will use. It is shown
// before anything is created, so a machine does not land in the wrong place.
// It is best effort: what cannot be found out is left out.
type AccountDescriber interface {
	DescribeAccount(ctx context.Context) string
}

// EgressAdviser is implemented by providers that can say how to give a subnet
// a way out, for the message shown when HasPrivateEgress reports none.
type EgressAdviser interface {
	// NATAdvice returns ready-to-read steps, each on its own line.
	NATAdvice(network, subnet string) string
}

// ConsoleReader is implemented by providers that can return an instance's
// serial console output. `up` reads it while waiting for the agent, to report
// a boot script that failed instead of waiting on a machine that will never
// connect.
type ConsoleReader interface {
	ConsoleOutput(ctx context.Context, id InstanceID) (string, error)
}

// ManagedInstance is a runlease-launched instance as the cloud reports it,
// found by its runlease-managed label rather than through the local registry.
type ManagedInstance struct {
	ID           InstanceID
	Provider     ProviderType
	Project      string // GCP only
	Region       string // AWS region or GCP zone
	Name         string // the manifest name it was launched under, if recorded
	InstanceType string
	State        InstanceStatus
	CreatedAt    time.Time
	ExpiresAt    *time.Time // from the runlease-expires-at label; nil if absent or unreadable
	CreatedBy    string     // the runlease-created-by label
	// DiskGB is the size of the attached disks, which keep billing while the
	// instance is stopped. 0 means the provider did not report it.
	DiskGB int64
	// DiskMonthlyUSD is a rough monthly cost of those disks at list price.
	// 0 means unknown.
	DiskMonthlyUSD float64
}

// ManagedLister is implemented by providers that can find every instance
// carrying the runlease-managed label, so that an instance the local registry
// has lost track of can still be seen and removed.
type ManagedLister interface {
	ListManaged(ctx context.Context) ([]ManagedInstance, error)
}

// ErrNoNetwork is returned (wrapped) by Preflighter.CheckNetwork when the
// network or subnet a launch would use is not there, as opposed to the check
// itself failing.
var ErrNoNetwork = errors.New("no network to launch on")

// Preflighter is implemented by providers that can check, without creating
// anything, the things a launch depends on. `runlease doctor` runs them. Every
// method makes read-only calls only.
type Preflighter interface {
	// CheckAccess makes one cheap call that proves the credentials work for
	// the project or account this provider acts in.
	CheckAccess(ctx context.Context) error
	// CheckRegion reports whether the provider's region or zone exists and
	// may be used with these credentials.
	CheckRegion(ctx context.Context) error
	// CheckNetwork reports whether the network a launch would land on exists,
	// and names it. With neither argument it is the one the provider picks by
	// default. network is GCP only. A missing network is an error wrapping
	// ErrNoNetwork.
	CheckNetwork(ctx context.Context, network, subnet string) (string, error)
}
