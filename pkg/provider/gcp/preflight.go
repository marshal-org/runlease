package gcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"golang.org/x/oauth2/google"
	"google.golang.org/api/compute/v1"
	"google.golang.org/api/googleapi"

	"github.com/marshal-org/runlease/pkg/provider"
)

// CheckAccess gets an access token, which fails only if the sign-in does, and
// then reads the project: the cheapest call that needs a project that exists,
// permission to use it, and the Compute Engine API turned on.
func (p *Provider) CheckAccess(ctx context.Context) error {
	if p.checkToken != nil {
		if err := p.checkToken(ctx); err != nil {
			return fmt.Errorf("getting an access token: %w", err)
		}
	}
	_, err := p.svc.Projects.Get(p.projectID).Fields("name").Context(ctx).Do()
	var gerr *googleapi.Error
	switch {
	case err == nil:
		return nil
	case !errors.As(err, &gerr):
		return fmt.Errorf("reading project %s: %w", p.projectID, err)
	case apiDisabled(gerr):
		return fmt.Errorf("%w for project %s: %w", provider.ErrServiceDisabled, p.projectID, err)
	case gerr.Code == http.StatusNotFound || gerr.Code == http.StatusForbidden:
		return fmt.Errorf("%w: project %s does not exist, or this sign-in may not use it: %w", provider.ErrProject, p.projectID, err)
	}
	return fmt.Errorf("reading project %s: %w", p.projectID, err)
}

// adcToken gets an access token from the application default credentials.
func adcToken(ctx context.Context) error {
	creds, err := google.FindDefaultCredentials(ctx, compute.ComputeScope)
	if err != nil {
		return err
	}
	_, err = creds.TokenSource.Token()
	return err
}

// apiDisabled reports whether Google refused the call because the API is not
// enabled for the project, which it also answers with a 403.
func apiDisabled(gerr *googleapi.Error) bool {
	for _, e := range gerr.Errors {
		if e.Reason == "accessNotConfigured" {
			return true
		}
	}
	for _, d := range gerr.Details {
		if m, ok := d.(map[string]any); ok && m["reason"] == "SERVICE_DISABLED" {
			return true
		}
	}
	return false
}

// CheckRegion reads the zone this provider launches in.
func (p *Provider) CheckRegion(ctx context.Context) error {
	_, err := p.svc.Zones.Get(p.projectID, p.zone).Fields("name").Context(ctx).Do()
	if isNotFound(err) {
		return fmt.Errorf("%s is not a zone project %s can use (zones look like us-central1-a): %w", p.zone, p.projectID, err)
	}
	if err != nil {
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
