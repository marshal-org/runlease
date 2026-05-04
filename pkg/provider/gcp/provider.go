package gcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode"

	"google.golang.org/api/compute/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"

	"github.com/runlease/runlease/pkg/provider"
)

// Provider implements CloudProvider for GCP Compute Engine spot VMs.
//
// Authentication uses Application Default Credentials (ADC):
//   gcloud auth application-default login     (developer workstation)
//   GOOGLE_APPLICATION_CREDENTIALS=key.json  (service account key file)
//   Attached service account                  (if running on GCP)
//
// The manifest `region` field is treated as a GCP zone (e.g. "us-central1-a").
type Provider struct {
	svc          *compute.Service
	projectID    string
	zone         string
	pollInterval time.Duration
}

func New(ctx context.Context, zone string) (*Provider, error) {
	projectID, err := resolveProjectID(ctx)
	if err != nil {
		return nil, err
	}
	svc, err := compute.NewService(ctx, option.WithScopes(compute.ComputeScope))
	if err != nil {
		return nil, fmt.Errorf("creating compute service: %w", err)
	}
	return &Provider{
		svc:          svc,
		projectID:    projectID,
		zone:         zone,
		pollInterval: 5 * time.Second,
	}, nil
}

func (p *Provider) RequestSpot(ctx context.Context, spec provider.InstanceSpec) (*provider.InstanceHandle, error) {
	name := generateInstanceName(spec.ManifestName)

	inst := &compute.Instance{
		Name:        name,
		MachineType: fmt.Sprintf("zones/%s/machineTypes/%s", p.zone, spec.InstanceType),
		Scheduling: &compute.Scheduling{
			ProvisioningModel: "SPOT",
			AutomaticRestart:  googleapi.Bool(false),
			OnHostMaintenance: "TERMINATE",
		},
		Disks: []*compute.AttachedDisk{
			{
				Boot:       true,
				AutoDelete: true,
				InitializeParams: &compute.AttachedDiskInitializeParams{
					SourceImage: spec.ImageID,
				},
			},
		},
		NetworkInterfaces: []*compute.NetworkInterface{
			gcpNetworkInterface(spec.PublicIP),
		},
		Metadata: &compute.Metadata{
			Items: []*compute.MetadataItems{
				{Key: "startup-script", Value: &spec.UserData},
			},
		},
	}

	if spec.SubnetID != "" {
		inst.NetworkInterfaces[0].Subnetwork = spec.SubnetID
	}
	if len(spec.NetworkTags) > 0 {
		inst.Tags = &compute.Tags{Items: spec.NetworkTags}
	}
	if spec.ServiceAccountEmail != "" {
		inst.ServiceAccounts = []*compute.ServiceAccount{
			{
				Email:  spec.ServiceAccountEmail,
				Scopes: []string{"https://www.googleapis.com/auth/cloud-platform"},
			},
		}
	}
	if len(spec.Tags) > 0 {
		inst.Labels = gcpLabels(spec.Tags)
	}

	op, err := p.svc.Instances.Insert(p.projectID, p.zone, inst).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("insert instance: %w", err)
	}
	if err := p.waitForZoneOp(ctx, op.Name); err != nil {
		return nil, fmt.Errorf("instance creation: %w", err)
	}

	return &provider.InstanceHandle{
		ID:           provider.InstanceID(name),
		Provider:     provider.ProviderGCP,
		Region:       p.zone,
		State:        provider.StatusPending,
		ManifestName: spec.ManifestName,
		CreatedAt:    time.Now().UTC(),
		Tags:         spec.Tags,
	}, nil
}

func (p *Provider) Terminate(ctx context.Context, id provider.InstanceID) error {
	op, err := p.svc.Instances.Delete(p.projectID, p.zone, string(id)).Context(ctx).Do()
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("delete instance: %w", err)
	}
	return p.waitForZoneOp(ctx, op.Name)
}

func (p *Provider) Status(ctx context.Context, id provider.InstanceID) (provider.InstanceStatus, error) {
	inst, err := p.svc.Instances.Get(p.projectID, p.zone, string(id)).Context(ctx).Do()
	if err != nil {
		if isNotFound(err) {
			return provider.StatusTerminated, nil
		}
		return provider.StatusUnknown, fmt.Errorf("get instance: %w", err)
	}
	return gcpStatus(inst.Status), nil
}

func (p *Provider) WaitUntilRunning(ctx context.Context, id provider.InstanceID) (*provider.InstanceHandle, error) {
	ticker := time.NewTicker(p.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			inst, err := p.svc.Instances.Get(p.projectID, p.zone, string(id)).Context(ctx).Do()
			if err != nil {
				return nil, fmt.Errorf("describe instance: %w", err)
			}
			switch gcpStatus(inst.Status) {
			case provider.StatusRunning:
				return gcpHandle(inst, p.zone), nil
			case provider.StatusTerminated, provider.StatusStopped:
				return nil, fmt.Errorf("instance %s entered state %s", id, inst.Status)
			}
		}
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func gcpHandle(inst *compute.Instance, zone string) *provider.InstanceHandle {
	h := &provider.InstanceHandle{
		ID:       provider.InstanceID(inst.Name),
		Provider: provider.ProviderGCP,
		Region:   zone,
		State:    gcpStatus(inst.Status),
	}
	if len(inst.NetworkInterfaces) > 0 {
		h.PrivateIP = inst.NetworkInterfaces[0].NetworkIP
		if len(inst.NetworkInterfaces[0].AccessConfigs) > 0 {
			h.PublicIP = inst.NetworkInterfaces[0].AccessConfigs[0].NatIP
		}
	}
	return h
}

func gcpStatus(s string) provider.InstanceStatus {
	switch s {
	case "PROVISIONING", "STAGING":
		return provider.StatusPending
	case "RUNNING":
		return provider.StatusRunning
	case "STOPPING", "STOPPED", "SUSPENDED":
		return provider.StatusStopped
	case "TERMINATED":
		return provider.StatusTerminated
	default:
		return provider.StatusUnknown
	}
}

func (p *Provider) waitForZoneOp(ctx context.Context, opName string) error {
	for {
		op, err := p.svc.ZoneOperations.Get(p.projectID, p.zone, opName).Context(ctx).Do()
		if err != nil {
			return fmt.Errorf("get operation: %w", err)
		}
		if op.Status == "DONE" {
			if op.Error != nil && len(op.Error.Errors) > 0 {
				return fmt.Errorf("operation failed: %s", op.Error.Errors[0].Message)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(p.pollInterval):
		}
	}
}

// generateInstanceName produces a GCP-valid name: lowercase alphanumeric +
// hyphens, max 63 chars, must start with a letter. Format: rl-<name>-<hex6>.
func generateInstanceName(manifestName string) string {
	sanitized := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return '-'
	}, manifestName)
	sanitized = strings.Trim(sanitized, "-")
	if len(sanitized) > 40 {
		sanitized = sanitized[:40]
	}
	b := make([]byte, 3)
	rand.Read(b) //nolint:errcheck
	return fmt.Sprintf("rl-%s-%s", sanitized, hex.EncodeToString(b))
}

func gcpNetworkInterface(publicIP bool) *compute.NetworkInterface {
	ni := &compute.NetworkInterface{}
	if publicIP {
		ni.AccessConfigs = []*compute.AccessConfig{
			{Type: "ONE_TO_ONE_NAT", Name: "External NAT"},
		}
	}
	return ni
}

func resolveProjectID(ctx context.Context) (string, error) {
	if p := os.Getenv("GOOGLE_CLOUD_PROJECT"); p != "" {
		return p, nil
	}
	if p := os.Getenv("GCLOUD_PROJECT"); p != "" {
		return p, nil
	}
	if _, err := exec.LookPath("gcloud"); err != nil {
		return "", fmt.Errorf("GCP project ID not set: export GOOGLE_CLOUD_PROJECT=<project-id>")
	}
	out, err := exec.CommandContext(ctx, "gcloud", "config", "get-value", "project").Output()
	if err == nil {
		if p := strings.TrimSpace(string(out)); p != "" && p != "(unset)" {
			return p, nil
		}
	}
	return "", fmt.Errorf("GCP project not configured: run 'gcloud config set project <project-id>' or export GOOGLE_CLOUD_PROJECT=<project-id>")
}

// gcpLabels converts a generic tag map to GCP label format.
// GCP label keys/values must match [a-z0-9_-]* and be ≤63 chars.
func gcpLabels(tags map[string]string) map[string]string {
	out := make(map[string]string, len(tags))
	for k, v := range tags {
		out[gcpSanitize(k)] = gcpSanitize(v)
	}
	return out
}

func gcpSanitize(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	v := strings.Trim(b.String(), "-")
	if len(v) > 63 {
		v = v[:63]
	}
	if v == "" {
		return "unknown"
	}
	return v
}

func isNotFound(err error) bool {
	var e *googleapi.Error
	if ok := errors.As(err, &e); ok {
		return e.Code == 404
	}
	return false
}
