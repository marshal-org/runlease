package pricing

import "strings"

// RemediationHint returns a short, actionable fix for the common
// credential/API errors behind a failed spot-price lookup, or "" when there's
// no specific guidance. Callers surface it next to the "price unavailable"
// warning so users know how to get live rates.
func RemediationHint(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	switch {
	// GCP: Cloud Billing Catalog API not enabled on the project.
	case strings.Contains(msg, "Cloud Billing API has not been used"),
		strings.Contains(msg, "SERVICE_DISABLED"),
		strings.Contains(msg, "accessNotConfigured"):
		return "enable the Cloud Billing API: gcloud services enable cloudbilling.googleapis.com"
	// GCP: authenticated but lacking catalog read access.
	case strings.Contains(msg, "PERMISSION_DENIED") && strings.Contains(msg, "billing"),
		strings.Contains(msg, "cloudbilling.skus.list"):
		return "grant your account the Billing Account Viewer role (cloudbilling.skus.list)"
	// GCP: no usable ADC at all.
	case strings.Contains(msg, "could not find default credentials"),
		strings.Contains(msg, "gcp project not"):
		return "set up credentials: gcloud auth application-default login"
	// AWS: IAM identity can't read spot prices.
	case strings.Contains(msg, "DescribeSpotPriceHistory") &&
		(strings.Contains(msg, "UnauthorizedOperation") || strings.Contains(msg, "not authorized") || strings.Contains(msg, "AccessDenied")):
		return "grant ec2:DescribeSpotPriceHistory to your IAM identity"
	}
	return ""
}
