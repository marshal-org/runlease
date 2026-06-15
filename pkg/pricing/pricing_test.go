package pricing

import (
	"os"
	"testing"
	"time"

	"github.com/runlease/runlease/pkg/provider"
)

func TestLowestSpot(t *testing.T) {
	tests := []struct {
		name    string
		in      []string
		want    float64
		wantErr bool
	}{
		{"single", []string{"0.0123"}, 0.0123, false},
		{"lowest across azs", []string{"0.05", "0.0123", "0.09"}, 0.0123, false},
		{"skips junk and zero", []string{"oops", "0", "0.02"}, 0.02, false},
		{"all invalid", []string{"oops", "0", "-1"}, 0, true},
		{"empty", nil, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := lowestSpot(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestComputeGCPHourly(t *testing.T) {
	// e2-standard-2: 2 vCPU, 8 GiB. coreRate 0.006, ramRate 0.001.
	// expect 2*0.006 + 8*0.001 = 0.020
	e2 := []gcpSKU{
		{Description: "Spot Preemptible E2 Instance Core running in Americas", UnitUSD: 0.006},
		{Description: "Spot Preemptible E2 Instance Ram running in Americas", UnitUSD: 0.001},
		{Description: "Spot Preemptible N2 Instance Core running in Americas", UnitUSD: 0.99},
		{Description: "Spot Preemptible E2 Custom Instance Core running in Americas", UnitUSD: 0.99},
	}
	got, err := computeGCPHourly("e2", 2, 8, e2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !approxEq(got, 0.020) {
		t.Fatalf("e2: got %v, want 0.020", got)
	}

	// c2 uses the "Compute optimized" phrase rather than a "C2" code.
	c2 := []gcpSKU{
		{Description: "Spot Preemptible Compute optimized Core running in Americas", UnitUSD: 0.01},
		{Description: "Spot Preemptible Compute optimized Ram running in Americas", UnitUSD: 0.002},
	}
	got, err = computeGCPHourly("c2", 4, 16, c2)
	if err != nil {
		t.Fatalf("unexpected c2 error: %v", err)
	}
	if !approxEq(got, 4*0.01+16*0.002) {
		t.Fatalf("c2: got %v, want %v", got, 4*0.01+16*0.002)
	}
}

func TestComputeGCPHourly_N2NotMatchedByN2D(t *testing.T) {
	// Only N2D SKUs present — an N2 request must NOT match them.
	skus := []gcpSKU{
		{Description: "Spot Preemptible N2D AMD Instance Core running in Americas", UnitUSD: 0.005},
		{Description: "Spot Preemptible N2D AMD Instance Ram running in Americas", UnitUSD: 0.001},
	}
	if _, err := computeGCPHourly("n2", 2, 8, skus); err == nil {
		t.Fatal("expected no-match error for n2 against n2d-only SKUs")
	}
}

func TestComputeGCPHourly_MissingRam(t *testing.T) {
	skus := []gcpSKU{{Description: "Spot Preemptible E2 Instance Core running in Americas", UnitUSD: 0.006}}
	if _, err := computeGCPHourly("e2", 2, 8, skus); err == nil {
		t.Fatal("expected error when ram SKU is missing")
	}
}

func TestZoneToRegion(t *testing.T) {
	cases := map[string]string{
		"us-central1-a":  "us-central1",
		"europe-west1-b": "europe-west1",
		"nodash":         "nodash",
	}
	for in, want := range cases {
		if got := zoneToRegion(in); got != want {
			t.Errorf("zoneToRegion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLeaseCostAndFormat(t *testing.T) {
	r := Rate{USDPerHour: 0.02}
	if got := r.LeaseCost(2 * time.Hour); !approxEq(got, 0.04) {
		t.Fatalf("LeaseCost = %v, want 0.04", got)
	}
	if got := r.LeaseCost(0); got != 0 {
		t.Fatalf("LeaseCost(0) = %v, want 0", got)
	}
	if got := FormatHourly(r); got != "$0.020/hr" {
		t.Fatalf("FormatHourly = %q", got)
	}
	approx := Rate{USDPerHour: 0.02, Approx: true}
	if got := FormatHourly(approx); got != "~$0.020/hr" {
		t.Fatalf("FormatHourly(approx) = %q", got)
	}
	if got := FormatLease(approx, 2*time.Hour); got != "~$0.040" {
		t.Fatalf("FormatLease(approx) = %q", got)
	}
}

func TestCacheRoundTrip(t *testing.T) {
	home := t.TempDir()
	r := Rate{
		Provider:     provider.ProviderAWS,
		InstanceType: "t3.small",
		Region:       "us-east-1",
		USDPerHour:   0.0072,
		FetchedAt:    time.Now().UTC(),
	}
	cacheStore(home, r)

	got, ok := cacheLookup(home, provider.ProviderAWS, "t3.small", "us-east-1", cacheTTL)
	if !ok {
		t.Fatal("expected cache hit")
	}
	if !approxEq(got.USDPerHour, 0.0072) {
		t.Fatalf("cached rate = %v", got.USDPerHour)
	}

	// Miss for a different key.
	if _, ok := cacheLookup(home, provider.ProviderAWS, "t3.large", "us-east-1", cacheTTL); ok {
		t.Fatal("expected miss for different type")
	}
}

func TestCacheStaleness(t *testing.T) {
	home := t.TempDir()
	stale := Rate{
		Provider:     provider.ProviderGCP,
		InstanceType: "e2-small",
		Region:       "us-central1",
		USDPerHour:   0.005,
		FetchedAt:    time.Now().Add(-48 * time.Hour),
	}
	cacheStore(home, stale)

	// Fresh lookup (24h TTL) misses the stale entry.
	if _, ok := cacheLookup(home, provider.ProviderGCP, "e2-small", "us-central1", cacheTTL); ok {
		t.Fatal("stale entry should miss the freshness check")
	}
	// maxAge 0 disables the check (stale fallback path).
	if _, ok := cacheLookup(home, provider.ProviderGCP, "e2-small", "us-central1", 0); !ok {
		t.Fatal("stale fallback should return the entry")
	}
}

func TestCacheCorruptFile(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(cachePath(home), []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	// Corrupt cache must behave as empty, not panic.
	if _, ok := cacheLookup(home, provider.ProviderAWS, "t3.small", "us-east-1", cacheTTL); ok {
		t.Fatal("corrupt cache should yield a miss")
	}
}

func TestRemediationHint(t *testing.T) {
	cases := []struct {
		msg  string
		want string
	}{
		{"listing compute SKUs: googleapi: Error 403: Cloud Billing API has not been used in project 123", "enable the Cloud Billing API: gcloud services enable cloudbilling.googleapis.com"},
		{"googleapi: Error 403: SERVICE_DISABLED", "enable the Cloud Billing API: gcloud services enable cloudbilling.googleapis.com"},
		{"PERMISSION_DENIED on billing account", "grant your account the Billing Account Viewer role (cloudbilling.skus.list)"},
		{"DescribeSpotPriceHistory: UnauthorizedOperation: not authorized", "grant ec2:DescribeSpotPriceHistory to your IAM identity"},
		{"some unrelated network timeout", ""},
	}
	for _, c := range cases {
		if got := RemediationHint(fmtErr(c.msg)); got != c.want {
			t.Errorf("RemediationHint(%q) = %q, want %q", c.msg, got, c.want)
		}
	}
	if RemediationHint(nil) != "" {
		t.Error("nil error should yield no hint")
	}
}

func fmtErr(s string) error { return &simpleErr{s} }

type simpleErr struct{ s string }

func (e *simpleErr) Error() string { return e.s }

func approxEq(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-9
}
