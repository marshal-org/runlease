package gcp

import (
	"testing"
	"time"

	"google.golang.org/api/compute/v1"

	"github.com/marshal-org/runlease/pkg/provider"
)

func TestManagedFromInstance(t *testing.T) {
	inst := &compute.Instance{
		Name:              "rl-run-62b18412-0e2f78",
		Zone:              "https://www.googleapis.com/compute/v1/projects/p/zones/europe-west1-b",
		MachineType:       "https://www.googleapis.com/compute/v1/projects/p/zones/europe-west1-b/machineTypes/e2-small",
		Status:            "TERMINATED",
		CreationTimestamp: "2026-10-13T08:00:00.000-07:00",
		Labels: map[string]string{
			"runlease-managed":    "true",
			"runlease-created-by": "ada",
			"runlease-expires-at": "2026-10-13t17-00-00z",
		},
		Disks: []*compute.AttachedDisk{{DiskSizeGb: 50}},
	}
	m := managedFromInstance(inst, "p")
	if m.ID != "rl-run-62b18412-0e2f78" || m.Region != "europe-west1-b" || m.Project != "p" || m.InstanceType != "e2-small" {
		t.Errorf("identity fields wrong: %+v", m)
	}
	// Powered off, not gone: this is the state that leaks a billed disk.
	if m.State != provider.StatusStopped {
		t.Errorf("State = %q, want stopped", m.State)
	}
	if m.Name != "run-62b18412" || m.CreatedBy != "ada" || m.DiskGB != 50 {
		t.Errorf("name/creator/disk wrong: %+v", m)
	}
	want := time.Date(2026, 10, 13, 17, 0, 0, 0, time.UTC)
	if m.ExpiresAt == nil || !m.ExpiresAt.Equal(want) {
		t.Errorf("ExpiresAt = %v, want %v", m.ExpiresAt, want)
	}
	if !m.CreatedAt.Equal(time.Date(2026, 10, 13, 15, 0, 0, 0, time.UTC)) {
		t.Errorf("CreatedAt = %v", m.CreatedAt)
	}
}

// The label must read back as the time that was written through gcpSanitize.
func TestParseExpiresLabel_roundTrip(t *testing.T) {
	when := time.Date(2026, 11, 15, 12, 30, 45, 0, time.UTC)
	got := parseExpiresLabel(gcpSanitize(when.Format(time.RFC3339)))
	if got == nil || !got.Equal(when) {
		t.Fatalf("round trip = %v, want %v", got, when)
	}
	for _, bad := range []string{"", "soon", "unknown"} {
		if parseExpiresLabel(bad) != nil {
			t.Errorf("parseExpiresLabel(%q) should be nil", bad)
		}
	}
}

func TestNameFromInstanceName(t *testing.T) {
	for in, want := range map[string]string{
		"rl-dev-a1b2c3":           "dev",
		"rl-my-runner-0e2f78":     "my-runner",
		"rl-odd":                  "odd",
		"something-else-entirely": "something-else-entirely",
	} {
		if got := nameFromInstanceName(in); got != want {
			t.Errorf("nameFromInstanceName(%q) = %q, want %q", in, got, want)
		}
	}
}
