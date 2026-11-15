package gcp

import (
	"context"
	"fmt"

	"github.com/marshal-org/runlease/pkg/provider"
)

// ConsoleOutput returns the tail of the instance's serial console (GCP keeps
// about the last megabyte), which is where the boot script's output goes.
func (p *Provider) ConsoleOutput(ctx context.Context, id provider.InstanceID) (string, error) {
	out, err := p.svc.Instances.GetSerialPortOutput(p.projectID, p.zone, string(id)).Context(ctx).Do()
	if err != nil {
		return "", fmt.Errorf("serial console: %w", err)
	}
	return out.Contents, nil
}
