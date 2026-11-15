// Package pricing resolves spot/preemptible hourly rates for launched
// instances using the caller's own cloud credentials (the same ADC / AWS
// default chain the provider packages use). It is strictly best-effort: every
// entry point returns an error rather than panicking, and callers are expected
// to treat any error as "pricing unavailable — skip it". Results are cached
// on disk for a day so repeated `list`/`up` calls don't re-hit the cloud APIs.
package pricing

import (
	"context"
	"fmt"
	"time"

	"github.com/marshal-org/runlease/pkg/provider"
)

// cacheTTL is how long a fetched rate is considered fresh. Spot prices drift
// slowly (AWS a few times/day, GCP roughly monthly), so a daily refresh is
// plenty — and it keeps `runlease list` from making a cloud call per row.
const cacheTTL = 24 * time.Hour

// Rate is a resolved spot price for one instance type in one region.
type Rate struct {
	Provider     provider.ProviderType `json:"provider"`
	InstanceType string                `json:"instanceType"`
	Region       string                `json:"region"`
	USDPerHour   float64               `json:"usdPerHour"`
	// Approx is set when the figure is a best-effort estimate rather than an
	// exact match (e.g. GCP shared-core machine types billed fractionally).
	Approx    bool      `json:"approx,omitempty"`
	FetchedAt time.Time `json:"fetchedAt"`
}

// LeaseCost returns the projected cost of holding the instance for d at this
// rate (e.g. the TTL set at launch).
func (r Rate) LeaseCost(d time.Duration) float64 {
	if d <= 0 {
		return 0
	}
	return r.USDPerHour * d.Hours()
}

// SpotRate returns the spot hourly rate for the given instance type/region,
// using a 24h on-disk cache rooted at home (the runlease home dir). On a cache
// miss it fetches live using the caller's cloud credentials and writes the
// result back. If the live fetch fails but a stale cached value exists, the
// stale value is returned (so a transient API/credential hiccup doesn't drop
// pricing entirely). Any other failure returns an error for the caller to skip.
func SpotRate(ctx context.Context, home string, prov provider.ProviderType, instanceType, region string) (Rate, error) {
	if instanceType == "" || region == "" {
		return Rate{}, fmt.Errorf("pricing: instance type and region required")
	}

	if r, ok := cacheLookup(home, prov, instanceType, region, cacheTTL); ok {
		return r, nil
	}

	usd, approx, err := fetchSpot(ctx, prov, instanceType, region)
	if err != nil {
		// Fall back to any stale cached value before giving up.
		if r, ok := cacheLookup(home, prov, instanceType, region, 0); ok {
			return r, nil
		}
		return Rate{}, err
	}

	rate := Rate{
		Provider:     prov,
		InstanceType: instanceType,
		Region:       region,
		USDPerHour:   usd,
		Approx:       approx,
		FetchedAt:    time.Now().UTC(),
	}
	cacheStore(home, rate)
	return rate, nil
}

// fetchSpot dispatches to the provider-specific live lookup.
func fetchSpot(ctx context.Context, prov provider.ProviderType, instanceType, region string) (usd float64, approx bool, err error) {
	switch prov {
	case provider.ProviderAWS:
		return fetchAWSSpot(ctx, instanceType, region)
	case provider.ProviderGCP:
		return fetchGCPSpot(ctx, instanceType, region)
	default:
		return 0, false, fmt.Errorf("pricing: unsupported provider %q", prov)
	}
}

// FormatHourly renders a rate as a compact dollar figure for tables/status
// lines, prefixing "~" when the figure is approximate (e.g. "$0.009/hr",
// "~$0.019/hr").
func FormatHourly(r Rate) string {
	return formatUSD(r.USDPerHour, r.Approx) + "/hr"
}

// FormatLease renders the projected cost of holding the instance for d.
func FormatLease(r Rate, d time.Duration) string {
	return formatUSD(r.LeaseCost(d), r.Approx)
}

func formatUSD(v float64, approx bool) string {
	prefix := "$"
	if approx {
		prefix = "~$"
	}
	return fmt.Sprintf("%s%.3f", prefix, v)
}
