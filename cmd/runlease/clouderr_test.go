package main

import (
	"errors"
	"strings"
	"testing"
)

func TestRegionFits(t *testing.T) {
	cases := []struct {
		prov, region string
		want         bool
	}{
		{"gcp", "us-central1-a", true},
		{"gcp", "europe-west1", true},
		{"gcp", "us-east-1", false},
		{"gcp", "eu-north-1", false},
		{"aws", "us-east-1", true},
		{"aws", "eu-north-1", true},
		{"aws", "us-gov-west-1", true},
		{"aws", "us-central1-a", false},
		{"aws", "europe-west1-b", false},
	}
	for _, c := range cases {
		if got := regionFits(c.prov, c.region); got != c.want {
			t.Errorf("regionFits(%q, %q) = %v, want %v", c.prov, c.region, got, c.want)
		}
	}
}

func TestExplainCloudError(t *testing.T) {
	cases := []struct{ in, want string }{
		{`insert instance: Post "https://compute.googleapis.com/...": auth: "invalid_grant" "reauth related error (invalid_rapt)"`, "gcloud auth application-default login"},
		{`google: could not find default credentials`, "gcloud auth application-default login"},
		{`operation error EC2: DescribeImages, get identity: get credentials: failed to refresh cached credentials`, "AWS sign-in"},
		{`Error 400: Invalid value for field 'resource.networkInterfaces[0].network': ''. The referenced network resource cannot be found.`, "--network NAME"},
		{`Post "https://ec2.us-central1-a.amazonaws.com/": dial tcp: lookup ec2.us-central1-a.amazonaws.com: no such host`, "--region us-east-1"},
	}
	for _, c := range cases {
		got := explainCloudError(errors.New(c.in)).Error()
		if !strings.Contains(got, c.want) || !strings.Contains(got, c.in) {
			t.Errorf("explainCloudError(%q) = %q, want the original text plus %q", c.in, got, c.want)
		}
	}
	plain := errors.New("quota exceeded")
	if explainCloudError(plain) != plain {
		t.Error("an error with no known cause must pass through unchanged")
	}
	if !isSignInError(errors.New(`auth: "invalid_grant"`)) || isSignInError(plain) {
		t.Error("isSignInError misclassified")
	}
}
