package pricing

import (
	"context"
	"fmt"
	"strconv"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// fetchAWSSpot returns the current Linux/UNIX spot price for instanceType in
// region via DescribeSpotPriceHistory, taking the lowest price across the
// region's availability zones (what you'd most likely pay). Uses the standard
// AWS default credential chain — the same one used to launch the instance.
func fetchAWSSpot(ctx context.Context, instanceType, region string) (float64, bool, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return 0, false, fmt.Errorf("aws config: %w", err)
	}
	client := ec2.NewFromConfig(cfg)

	out, err := client.DescribeSpotPriceHistory(ctx, &ec2.DescribeSpotPriceHistoryInput{
		InstanceTypes:       []types.InstanceType{types.InstanceType(instanceType)},
		ProductDescriptions: []string{"Linux/UNIX"},
		// StartTime bounds the response to the price currently in effect rather
		// than up to 90 days of history. AWS returns the price valid at StartTime
		// (it does not require a price-change event exactly at this instant), so
		// a small lookback guarantees we capture the latest change even if the
		// price has been stable.
		StartTime: awssdk.Time(time.Now().Add(-6 * time.Hour)),
	})
	if err != nil {
		return 0, false, fmt.Errorf("DescribeSpotPriceHistory: %w", err)
	}

	entries := make([]spotEntry, 0, len(out.SpotPriceHistory))
	for _, p := range out.SpotPriceHistory {
		v, err := strconv.ParseFloat(awssdk.ToString(p.SpotPrice), 64)
		if err != nil || v <= 0 {
			continue
		}
		entries = append(entries, spotEntry{az: awssdk.ToString(p.AvailabilityZone), price: v, ts: awssdk.ToTime(p.Timestamp)})
	}
	usd, err := currentLowest(entries)
	if err != nil {
		return 0, false, fmt.Errorf("aws spot price for %s in %s: %w", instanceType, region, err)
	}
	return usd, false, nil
}

type spotEntry struct {
	az    string
	price float64
	ts    time.Time
}

// currentLowest returns the lowest *current* spot price across AZs: it first
// reduces each AZ to its most recent entry (history can contain several entries
// per AZ over the lookback window), then takes the minimum of those. Factored
// out for testing without hitting EC2.
func currentLowest(entries []spotEntry) (float64, error) {
	latest := make(map[string]spotEntry)
	for _, e := range entries {
		if cur, ok := latest[e.az]; !ok || e.ts.After(cur.ts) {
			latest[e.az] = e
		}
	}
	best := -1.0
	for _, e := range latest {
		if best < 0 || e.price < best {
			best = e.price
		}
	}
	if best < 0 {
		return 0, fmt.Errorf("no spot price data returned")
	}
	return best, nil
}
