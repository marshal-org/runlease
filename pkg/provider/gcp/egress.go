package gcp

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/api/compute/v1"
)

// HasPrivateEgress reports whether an instance with no public IP on the given
// network / subnetwork would reach the internet through Cloud NAT. It is what
// lets `up` refuse, before creating anything, a machine that could never dial
// the relay.
//
// Only Cloud NAT is recognised. A network that gets out some other way (a NAT
// instance, a proxy, a VPN) reports false; `publicIp: false` in the manifest
// skips the check for those.
func (p *Provider) HasPrivateEgress(ctx context.Context, network, subnet string) (bool, error) {
	project, region, subnetName := p.subnetLocation(subnet)

	if subnet != "" {
		sn, err := p.svc.Subnetworks.Get(project, region, subnetName).Context(ctx).Do()
		if err != nil {
			return false, fmt.Errorf("reading subnetwork %s: %w", subnetName, err)
		}
		network = sn.Network
	}

	var routers []*compute.Router
	err := p.svc.Routers.List(project, region).Context(ctx).Pages(ctx, func(l *compute.RouterList) error {
		routers = append(routers, l.Items...)
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("listing Cloud Routers in %s: %w", region, err)
	}
	return natCovers(routers, network, subnetName), nil
}

// subnetLocation works out where the subnetwork lives. A bare name is in this
// provider's project and the region of its zone; a self-link (as used for a
// Shared VPC) names its own project and region. An empty subnet means the
// auto-mode "default" network, whose subnetwork in every region is "default".
func (p *Provider) subnetLocation(subnet string) (project, region, name string) {
	project, region, name = p.projectID, regionOfZone(p.zone), "default"
	if subnet == "" {
		return
	}
	parts := strings.Split(subnet, "/")
	name = parts[len(parts)-1]
	for i := 0; i+1 < len(parts); i++ {
		switch parts[i] {
		case "projects":
			project = parts[i+1]
		case "regions":
			region = parts[i+1]
		}
	}
	return
}

func regionOfZone(zone string) string {
	if idx := strings.LastIndex(zone, "-"); idx >= 0 {
		return zone[:idx]
	}
	return zone
}

// natCovers reports whether any router on the network has a NAT gateway that
// serves the subnetwork. An empty network means the "default" network.
func natCovers(routers []*compute.Router, network, subnet string) bool {
	if network == "" {
		network = "default"
	}
	for _, r := range routers {
		if lastSegment(r.Network) != lastSegment(network) {
			continue
		}
		for _, nat := range r.Nats {
			if nat.SourceSubnetworkIpRangesToNat != "LIST_OF_SUBNETWORKS" {
				return true // every subnetwork of the network in this region
			}
			for _, s := range nat.Subnetworks {
				if lastSegment(s.Name) == subnet {
					return true
				}
			}
		}
	}
	return false
}

func lastSegment(path string) string {
	if idx := strings.LastIndex(path, "/"); idx >= 0 {
		return path[idx+1:]
	}
	return path
}
