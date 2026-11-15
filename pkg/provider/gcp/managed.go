package gcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"google.golang.org/api/compute/v1"

	"github.com/marshal-org/runlease/pkg/provider"
)

// ListManaged returns every instance in the project, in any zone, that carries
// the runlease-managed label. It is one aggregated call, so it does not depend
// on knowing which zones were used.
func (p *Provider) ListManaged(ctx context.Context) ([]provider.ManagedInstance, error) {
	var out []provider.ManagedInstance
	err := p.svc.Instances.AggregatedList(p.projectID).
		Filter("labels.runlease-managed = true").
		Context(ctx).
		Pages(ctx, func(l *compute.InstanceAggregatedList) error {
			for _, scoped := range l.Items {
				for _, inst := range scoped.Instances {
					m := managedFromInstance(inst, p.projectID)
					if m.State == provider.StatusStopped {
						// Only stopped instances are reported with a cost, so
						// only they are worth the extra call for the disk type.
						m.DiskMonthlyUSD = p.diskMonthlyUSD(ctx, inst)
					}
					out = append(out, m)
				}
			}
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("listing runlease instances in %s: %w", p.projectID, err)
	}
	return out, nil
}

func managedFromInstance(inst *compute.Instance, project string) provider.ManagedInstance {
	m := provider.ManagedInstance{
		ID:           provider.InstanceID(inst.Name),
		Provider:     provider.ProviderGCP,
		Project:      project,
		Region:       lastSegment(inst.Zone),
		InstanceType: machineTypeName(inst.MachineType),
		State:        gcpStatus(inst.Status),
		CreatedBy:    inst.Labels["runlease-created-by"],
		ExpiresAt:    parseExpiresLabel(inst.Labels["runlease-expires-at"]),
		// The name label is not set on GCP; the instance name is rl-<name>-<suffix>.
		Name: nameFromInstanceName(inst.Name),
	}
	if t, err := time.Parse(time.RFC3339, inst.CreationTimestamp); err == nil {
		m.CreatedAt = t.UTC()
	}
	for _, d := range inst.Disks {
		m.DiskGB += d.DiskSizeGb
	}
	return m
}

// parseExpiresLabel reads the runlease-expires-at label. It is written as an
// RFC 3339 time and then squeezed into a GCP label, which lower-cases it and
// turns each colon into a dash: 2026-11-15t12-00-00z.
func parseExpiresLabel(v string) *time.Time {
	if v == "" {
		return nil
	}
	t, err := time.Parse("2006-01-02t15-04-05z", v)
	if err != nil {
		return nil
	}
	t = t.UTC()
	return &t
}

// nameFromInstanceName undoes generateInstanceName: rl-<name>-<6 hex>.
func nameFromInstanceName(n string) string {
	n = strings.TrimPrefix(n, "rl-")
	if i := strings.LastIndex(n, "-"); i > 0 && len(n)-i-1 == 6 {
		return n[:i]
	}
	return n
}

// Rough list prices per GB per month, in US regions. Other regions differ by
// a few cents; this is for an "about $N a month" figure, not a bill.
var diskUSDPerGBMonth = map[string]float64{
	"pd-standard": 0.04,
	"pd-balanced": 0.10,
	"pd-ssd":      0.17,
}

// diskMonthlyUSD estimates what the instance's persistent disks cost while it
// sits stopped. It returns 0 if a disk's type cannot be read or is not one it
// has a price for.
func (p *Provider) diskMonthlyUSD(ctx context.Context, inst *compute.Instance) float64 {
	var total float64
	for _, d := range inst.Disks {
		zone, name := lastSegment(inst.Zone), lastSegment(d.Source)
		if name == "" {
			return 0
		}
		disk, err := p.svc.Disks.Get(p.projectID, zone, name).Context(ctx).Do()
		if err != nil {
			return 0
		}
		price, ok := diskUSDPerGBMonth[lastSegment(disk.Type)]
		if !ok {
			return 0
		}
		total += price * float64(disk.SizeGb)
	}
	return total
}
