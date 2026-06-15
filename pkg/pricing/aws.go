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
		// StartTime = now asks for the prices currently in effect rather than a
		// historical window.
		StartTime: awssdk.Time(time.Now()),
	})
	if err != nil {
		return 0, false, fmt.Errorf("DescribeSpotPriceHistory: %w", err)
	}

	prices := make([]string, 0, len(out.SpotPriceHistory))
	for _, p := range out.SpotPriceHistory {
		prices = append(prices, awssdk.ToString(p.SpotPrice))
	}
	usd, err := lowestSpot(prices)
	if err != nil {
		return 0, false, fmt.Errorf("aws spot price for %s in %s: %w", instanceType, region, err)
	}
	return usd, false, nil
}

// lowestSpot parses spot-price strings and returns the lowest positive value.
// Factored out for testing without hitting EC2.
func lowestSpot(prices []string) (float64, error) {
	best := -1.0
	for _, s := range prices {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil || v <= 0 {
			continue
		}
		if best < 0 || v < best {
			best = v
		}
	}
	if best < 0 {
		return 0, fmt.Errorf("no spot price data returned")
	}
	return best, nil
}
