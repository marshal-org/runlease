package pricing

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"google.golang.org/api/cloudbilling/v1"
	"google.golang.org/api/compute/v1"
	"google.golang.org/api/option"
)

// computeEngineServiceID is the fixed Cloud Billing Catalog service ID for
// Compute Engine. SKUs (per-vCPU and per-GB-RAM rates) live under it.
const computeEngineServiceID = "6F81-5844-456A"

// gcpFamilyTokens maps a machine-type family (the prefix before the first "-",
// e.g. "e2" in "e2-standard-2") to the substrings that identify its SKU in the
// Billing Catalog. Matching is a heuristic over SKU descriptions, so only the
// families runlease's catalog actually launches are listed; anything else
// returns an error and pricing is skipped. Short codes are matched
// space-padded (" N2 " so it can't match "N2D"); phrases are matched loosely.
var gcpFamilyTokens = map[string][]string{
	"e2": {" E2 "},
	"n2": {" N2 "},
	"n1": {" N1 "},
	"c2": {" C2 ", "COMPUTE OPTIMIZED"},
}

// gcpSharedCore are machine types billed as a fraction of a vCPU. machineTypes
// reports their full guest CPU count, so a cores×rate estimate overshoots —
// we still report it, flagged approximate.
var gcpSharedCore = map[string]bool{
	"e2-micro":  true,
	"e2-small":  true,
	"e2-medium": true,
}

// fetchGCPSpot computes the preemptible (spot) hourly rate for machineType in
// the given zone, as vCPUs×coreRate + GB×ramRate using the Cloud Billing
// Catalog. vCPU/memory come from the authoritative machineTypes.get call.
// Reuses Application Default Credentials — the same auth used to launch.
//
// Note: GPU/accelerator cost is not included — the figure covers the base
// machine type only.
func fetchGCPSpot(ctx context.Context, machineType, zone string) (float64, bool, error) {
	family, _, ok := strings.Cut(machineType, "-")
	if !ok || gcpFamilyTokens[family] == nil {
		return 0, false, fmt.Errorf("gcp: unsupported machine family for %q", machineType)
	}
	region := zoneToRegion(zone)

	project, err := resolveGCPProject(ctx)
	if err != nil {
		return 0, false, err
	}

	cpus, memGiB, err := gcpMachineSpec(ctx, project, zone, machineType)
	if err != nil {
		return 0, false, err
	}

	skus, err := gcpPreemptibleSKUs(ctx, region)
	if err != nil {
		return 0, false, err
	}

	usd, err := computeGCPHourly(family, cpus, memGiB, skus)
	if err != nil {
		return 0, false, err
	}
	return usd, gcpSharedCore[machineType], nil
}

// gcpSKU is a region-filtered preemptible Compute SKU reduced to what the
// matcher needs.
type gcpSKU struct {
	Description string
	UnitUSD     float64 // price per vCPU-hour (core SKU) or per GiB-hour (ram SKU)
}

// computeGCPHourly finds the core and RAM SKU for family among the candidate
// SKUs and returns cpus×coreRate + memGiB×ramRate. Pure (no I/O) so it's unit
// tested with synthetic SKUs.
func computeGCPHourly(family string, cpus int64, memGiB float64, skus []gcpSKU) (float64, error) {
	tokens := gcpFamilyTokens[family]
	if tokens == nil {
		return 0, fmt.Errorf("unsupported machine family %q", family)
	}

	var coreUSD, ramUSD float64
	var haveCore, haveRam bool
	for _, s := range skus {
		d := " " + strings.ToUpper(s.Description) + " "
		if strings.Contains(d, "CUSTOM") || strings.Contains(d, "SOLE TENANCY") {
			continue
		}
		if !matchesAnyToken(d, tokens) {
			continue
		}
		switch {
		case strings.Contains(d, " CORE ") && !haveCore:
			coreUSD, haveCore = s.UnitUSD, true
		case strings.Contains(d, " RAM ") && !haveRam:
			ramUSD, haveRam = s.UnitUSD, true
		}
	}
	if !haveCore || !haveRam {
		return 0, fmt.Errorf("no preemptible core/ram SKU found for family %q", family)
	}
	return float64(cpus)*coreUSD + memGiB*ramUSD, nil
}

func matchesAnyToken(paddedUpperDesc string, tokens []string) bool {
	for _, t := range tokens {
		if strings.Contains(paddedUpperDesc, t) {
			return true
		}
	}
	return false
}

// gcpMachineSpec returns the vCPU count and memory (GiB) for a machine type.
func gcpMachineSpec(ctx context.Context, project, zone, machineType string) (cpus int64, memGiB float64, err error) {
	svc, err := compute.NewService(ctx, option.WithScopes(compute.ComputeReadonlyScope))
	if err != nil {
		return 0, 0, fmt.Errorf("gcp compute service: %w", err)
	}
	mt, err := svc.MachineTypes.Get(project, zone, machineType).Context(ctx).Do()
	if err != nil {
		return 0, 0, fmt.Errorf("gcp machineTypes.get %s: %w", machineType, err)
	}
	return mt.GuestCpus, float64(mt.MemoryMb) / 1024.0, nil
}

// gcpPreemptibleSKUs lists Compute Engine preemptible SKUs that apply to
// region, reduced to description + USD unit price.
func gcpPreemptibleSKUs(ctx context.Context, region string) ([]gcpSKU, error) {
	svc, err := cloudbilling.NewService(ctx, option.WithScopes(cloudbilling.CloudPlatformScope))
	if err != nil {
		return nil, fmt.Errorf("gcp billing service: %w", err)
	}

	var out []gcpSKU
	call := svc.Services.Skus.List("services/" + computeEngineServiceID).CurrencyCode("USD").PageSize(5000)
	err = call.Pages(ctx, func(page *cloudbilling.ListSkusResponse) error {
		for _, sku := range page.Skus {
			if sku.Category == nil || sku.Category.ResourceFamily != "Compute" || sku.Category.UsageType != "Preemptible" {
				continue
			}
			if !containsRegion(sku.ServiceRegions, region) {
				continue
			}
			price, ok := skuUnitUSD(sku)
			if !ok {
				continue
			}
			out = append(out, gcpSKU{Description: sku.Description, UnitUSD: price})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("listing compute SKUs: %w", err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no preemptible compute SKUs for region %s", region)
	}
	return out, nil
}

// skuUnitUSD pulls the marginal (last-tier) unit price out of a SKU in USD.
func skuUnitUSD(sku *cloudbilling.Sku) (float64, bool) {
	if len(sku.PricingInfo) == 0 {
		return 0, false
	}
	pe := sku.PricingInfo[0].PricingExpression
	if pe == nil || len(pe.TieredRates) == 0 {
		return 0, false
	}
	rate := pe.TieredRates[len(pe.TieredRates)-1]
	if rate.UnitPrice == nil {
		return 0, false
	}
	return float64(rate.UnitPrice.Units) + float64(rate.UnitPrice.Nanos)/1e9, true
}

func containsRegion(regions []string, region string) bool {
	for _, r := range regions {
		if r == region {
			return true
		}
	}
	return false
}

// zoneToRegion strips the zone suffix ("us-central1-a" → "us-central1"). If the
// input has no zone suffix it is returned unchanged.
func zoneToRegion(zone string) string {
	if i := strings.LastIndex(zone, "-"); i >= 0 {
		return zone[:i]
	}
	return zone
}

// resolveGCPProject finds the active GCP project the same way the provider
// does: env vars first, then `gcloud config`.
func resolveGCPProject(ctx context.Context) (string, error) {
	if p := os.Getenv("GOOGLE_CLOUD_PROJECT"); p != "" {
		return p, nil
	}
	if p := os.Getenv("GCLOUD_PROJECT"); p != "" {
		return p, nil
	}
	if _, err := exec.LookPath("gcloud"); err != nil {
		return "", fmt.Errorf("gcp project not set: export GOOGLE_CLOUD_PROJECT=<project-id>")
	}
	out, err := exec.CommandContext(ctx, "gcloud", "config", "get-value", "project").Output()
	if err == nil {
		if p := strings.TrimSpace(string(out)); p != "" && p != "(unset)" {
			return p, nil
		}
	}
	return "", fmt.Errorf("gcp project not configured")
}
