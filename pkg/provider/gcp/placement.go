package gcp

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"google.golang.org/api/compute/v1"
	"google.golang.org/api/googleapi"

	"github.com/marshal-org/runlease/pkg/provider"
)

// zoneSuffix matches the letter that turns a region into a zone.
var zoneSuffix = regexp.MustCompile(`-[a-z]$`)

// ResolvePlacement fills in what a launch needs and the user did not name:
//
//   - a region (europe-west4) becomes its first zone;
//   - with no network named and no "default" network in the project, the one
//     network or subnetwork a machine could join in the region is used;
//   - a custom-mode network named without a subnetwork gets its one
//     subnetwork in the region.
//
// When there is more than one candidate it does not guess: the error lists
// each as the flags to pass. Every call it makes is read-only.
func (p *Provider) ResolvePlacement(ctx context.Context, req provider.PlacementRequest) (*provider.Placement, error) {
	pl := &provider.Placement{Network: req.Network, Subnet: req.Subnet}

	region, zoneNamed := p.zone, zoneSuffix.MatchString(p.zone)
	if zoneNamed {
		region = regionOfZone(p.zone)
	}
	zones, err := p.zonesOf(ctx, region)
	if err != nil {
		return nil, err
	}
	zoneNote := ""
	if zoneNamed {
		if !contains(zones, p.zone) {
			return nil, fmt.Errorf("%w: %s has no zone %s. Its zones are: %s",
				provider.ErrBadRegion, region, p.zone, strings.Join(zones, ", "))
		}
	} else {
		p.zone = zones[0]
		zoneNote = fmt.Sprintf("zone %s (a zone was not named, so the first one in %s)", p.zone, region)
	}

	if req.Subnet != "" {
		if _, err := p.CheckNetwork(ctx, req.Network, req.Subnet); err != nil {
			return nil, err
		}
	} else {
		c, err := p.chooseNetwork(ctx, req.Network, region, req.AnyRegion)
		if err != nil {
			return nil, err
		}
		if c.region != "" && c.region != region {
			// Only reached with AnyRegion: the project's one subnetwork is elsewhere.
			if zones, err = p.zonesOf(ctx, c.region); err != nil {
				return nil, err
			}
			p.zone = zones[0]
			zoneNote = fmt.Sprintf("zone %s (this project's only subnetwork is in %s)", p.zone, c.region)
		}
		switch {
		case c.subnet != "":
			pl.Subnet = c.subnetRef(p.projectID)
			pl.Notes = append(pl.Notes, fmt.Sprintf("subnetwork %s on network %s (the only one in %s)", c.subnet, c.network, c.region))
		case req.Network == "" && c.network != "default":
			pl.Network = c.network
			pl.Notes = append(pl.Notes, fmt.Sprintf("network %s (this project has no 'default' network, and this is its only one)", c.network))
		}
	}
	if zoneNote != "" {
		pl.Notes = append([]string{zoneNote}, pl.Notes...)
	}
	pl.Region = p.zone
	return pl, nil
}

// zonesOf returns the names of a region's zones, in order.
func (p *Provider) zonesOf(ctx context.Context, region string) ([]string, error) {
	r, err := p.svc.Regions.Get(p.projectID, region).Fields("zones").Context(ctx).Do()
	if isNotFound(err) || isInvalid(err) {
		return nil, fmt.Errorf("%w: Google Cloud has no region %s for project %s (regions look like us-central1, zones like us-central1-a)",
			provider.ErrBadRegion, region, p.projectID)
	}
	if err != nil {
		return nil, fmt.Errorf("reading region %s: %w", region, err)
	}
	zones := make([]string, 0, len(r.Zones))
	for _, z := range r.Zones {
		zones = append(zones, lastSegment(z))
	}
	sort.Strings(zones)
	if len(zones) == 0 {
		return nil, fmt.Errorf("%w: region %s has no zones project %s can use", provider.ErrBadRegion, region, p.projectID)
	}
	return zones, nil
}

// netChoice is one place a machine could be attached: an auto-mode network
// (which has a subnetwork in every region), or one subnetwork of a
// custom-mode network.
type netChoice struct {
	network string
	subnet  string // empty for an auto-mode network
	link    string // the subnetwork's self-link
	region  string // empty for an auto-mode network
}

// subnetRef is how the subnetwork is passed on: its name when it is in the
// launching project, its self-link when it belongs to another one.
func (c netChoice) subnetRef(project string) string {
	if host := projectOfLink(c.link); host != "" && host != project {
		return c.link
	}
	return c.subnet
}

// flags is the choice written as what to pass to `runlease up`.
func (c netChoice) flags(withRegion bool) string {
	if c.subnet == "" {
		return "--network " + c.network
	}
	s := fmt.Sprintf("--subnet %s   (network %s)", c.subnet, c.network)
	if withRegion {
		s = "--region " + c.region + " " + s
	}
	return s
}

const manifestNetworkHint = "(in a manifest: gcp.network / gcp.subnetwork)"

// chooseNetwork picks where in region a machine attaches when no subnetwork
// was named. network is the one the user named, or "" for the project's
// "default" network and, failing that, whatever networks the project has.
func (p *Provider) chooseNetwork(ctx context.Context, network, region string, anyRegion bool) (netChoice, error) {
	project, pool, err := p.networkPool(ctx, network)
	if err != nil {
		return netChoice{}, err
	}
	var here, all []netChoice
	for _, n := range pool {
		if n.AutoCreateSubnetworks {
			c := netChoice{network: n.Name}
			here, all = append(here, c), append(all, c)
			continue
		}
		for _, s := range n.Subnetworks {
			c := netChoice{network: n.Name, subnet: lastSegment(s), link: s, region: regionOfLink(s)}
			all = append(all, c)
			if c.region == region {
				here = append(here, c)
			}
		}
	}
	switch {
	case len(here) == 1:
		return here[0], nil
	case len(here) > 1:
		return netChoice{}, fmt.Errorf("%w: project %s has %d places a machine could join in %s, so name one:\n%s\n%s",
			provider.ErrNoNetwork, project, len(here), region, flagLines(here, false), manifestNetworkHint)
	case len(all) == 0:
		return netChoice{}, fmt.Errorf("%w: %s in project %s has no subnetwork in any region. Add one:\n"+
			"  gcloud compute networks subnets create runlease --project %s --network %s --region %s --range 10.80.0.0/24",
			provider.ErrNoNetwork, networkNames(pool), project, project, pool[0].Name, region)
	case len(all) == 1 && anyRegion:
		return all[0], nil
	}
	return netChoice{}, fmt.Errorf("%w: project %s has no subnetwork in %s. It has these, so launch with one of:\n%s\n%s",
		provider.ErrNoNetwork, project, region, flagLines(all, true), manifestNetworkHint)
}

// networkPool returns the networks a launch may choose from, and the project
// they are in: the named one, else "default", else every network there is.
func (p *Provider) networkPool(ctx context.Context, network string) (string, []*compute.Network, error) {
	project, name := p.projectID, "default"
	if network != "" {
		name = lastSegment(network)
		if host := projectOfLink(network); host != "" {
			project = host
		}
	}
	const fields = "name,autoCreateSubnetworks,subnetworks"
	n, err := p.svc.Networks.Get(project, name).Fields(fields).Context(ctx).Do()
	if err == nil {
		return project, []*compute.Network{n}, nil
	}
	if !isNotFound(err) {
		return "", nil, fmt.Errorf("reading network %s: %w", name, err)
	}
	var pool []*compute.Network
	err = p.svc.Networks.List(project).Fields("items("+fields+")", "nextPageToken").Context(ctx).
		Pages(ctx, func(l *compute.NetworkList) error {
			pool = append(pool, l.Items...)
			return nil
		})
	if err != nil {
		return "", nil, fmt.Errorf("listing the networks of project %s: %w", project, err)
	}
	sort.Slice(pool, func(i, j int) bool { return pool[i].Name < pool[j].Name })
	switch {
	case len(pool) == 0:
		return "", nil, fmt.Errorf("%w: project %s has no VPC network. Create one:\n"+
			"  gcloud compute networks create default --project %s --subnet-mode auto\n"+
			"or, to use a Shared VPC, name its subnetwork:\n"+
			"  --subnet projects/HOST_PROJECT/regions/REGION/subnetworks/NAME",
			provider.ErrNoNetwork, project, project)
	case network != "":
		return "", nil, fmt.Errorf("%w: project %s has no network named %s. Its networks are: %s",
			provider.ErrNoNetwork, project, name, networkNames(pool))
	}
	return project, pool, nil
}

func networkNames(pool []*compute.Network) string {
	names := make([]string, 0, len(pool))
	for _, n := range pool {
		names = append(names, n.Name)
	}
	return strings.Join(names, ", ")
}

func flagLines(choices []netChoice, withRegion bool) string {
	lines := make([]string, 0, len(choices))
	for _, c := range choices {
		lines = append(lines, "  "+c.flags(withRegion))
	}
	return strings.Join(lines, "\n")
}

// regionOfLink returns the region named in a resource self-link, or "".
func regionOfLink(link string) string {
	parts := strings.Split(link, "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == "regions" {
			return parts[i+1]
		}
	}
	return ""
}

// isInvalid reports whether Google rejected a value in the request (a 400),
// which is how it answers a region name that is not written like one.
func isInvalid(err error) bool {
	var e *googleapi.Error
	return errors.As(err, &e) && e.Code == 400
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
