package gcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"google.golang.org/api/googleapi"

	"github.com/marshal-org/runlease/pkg/provider"
)

// CheckAccess reads the project. It is the cheapest call that needs working
// credentials, a project that exists, and the Compute Engine API turned on.
func (p *Provider) CheckAccess(ctx context.Context) error {
	if _, err := p.svc.Projects.Get(p.projectID).Fields("name").Context(ctx).Do(); err != nil {
		return fmt.Errorf("reading project %s: %w", p.projectID, err)
	}
	return nil
}

// CheckRegion reads the zone this provider launches in.
func (p *Provider) CheckRegion(ctx context.Context) error {
	if _, err := p.svc.Zones.Get(p.projectID, p.zone).Fields("name").Context(ctx).Do(); err != nil {
		if isNotFound(err) || isBadRequest(err) {
			return fmt.Errorf("%s is not a zone of Google Cloud that project %s can use (zones look like us-central1-a)", p.zone, p.projectID)
		}
		return fmt.Errorf("reading zone %s: %w", p.zone, err)
	}
	return nil
}

// CheckNetwork reads the subnetwork if one is named, and the network (the
// "default" one when none is named) otherwise. A custom-mode network with no
// subnetwork named counts as missing: Compute Engine cannot pick one itself.
func (p *Provider) CheckNetwork(ctx context.Context, network, subnet string) (string, error) {
	if subnet != "" {
		project, region, name := p.subnetLocation(network, subnet)
		sn, err := p.svc.Subnetworks.Get(project, region, name).Fields("name", "network").Context(ctx).Do()
		if isNotFound(err) {
			return "", fmt.Errorf("%w: project %s has no subnetwork %s in %s", provider.ErrNoNetwork, project, name, region)
		}
		if err != nil {
			return "", fmt.Errorf("reading subnetwork %s: %w", name, err)
		}
		return fmt.Sprintf("subnetwork %s (network %s, %s)", sn.Name, lastSegment(sn.Network), region), nil
	}

	project, name := p.projectID, "default"
	if network != "" {
		name = lastSegment(network)
		if host := projectOfLink(network); host != "" {
			project = host
		}
	}
	n, err := p.svc.Networks.Get(project, name).Fields("name", "autoCreateSubnetworks", "subnetworks").Context(ctx).Do()
	if isNotFound(err) {
		return "", fmt.Errorf("%w: project %s has no network named %s", provider.ErrNoNetwork, project, name)
	}
	if err != nil {
		return "", fmt.Errorf("reading network %s: %w", name, err)
	}
	if !n.AutoCreateSubnetworks {
		region := regionOfZone(p.zone)
		for _, s := range n.Subnetworks {
			if strings.Contains(s, "/regions/"+region+"/") {
				return "", fmt.Errorf("%w: network %s is custom-mode, so a subnetwork has to be named (it has %s in %s)",
					provider.ErrNoNetwork, name, lastSegment(s), region)
			}
		}
		return "", fmt.Errorf("%w: network %s is custom-mode and has no subnetwork in %s", provider.ErrNoNetwork, name, region)
	}
	return fmt.Sprintf("network %s (%s)", n.Name, regionOfZone(p.zone)), nil
}

// projectOfLink returns the project named in a resource self-link, or "".
func projectOfLink(link string) string {
	parts := strings.Split(link, "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == "projects" {
			return parts[i+1]
		}
	}
	return ""
}

func isBadRequest(err error) bool {
	var gerr *googleapi.Error
	return errors.As(err, &gerr) && gerr.Code == http.StatusBadRequest
}
