package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
)

// A cloud SDK error is accurate and unreadable. explainCloudError keeps the
// original text and adds one line saying what to do, for the failures a new
// user actually meets: an expired sign-in, a project with no default network,
// a region that belongs to the other cloud.
func explainCloudError(err error) error {
	if err == nil {
		return nil
	}
	if hint := cloudErrorHint(err.Error()); hint != "" {
		return fmt.Errorf("%w\n\n%s", err, hint)
	}
	return err
}

var errCloudSignIn = errors.New("cloud sign-in expired or missing")

// isSignInError reports whether err says the cloud credentials are expired or
// absent, as opposed to the request itself being wrong.
func isSignInError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errCloudSignIn) {
		return true
	}
	s := err.Error()
	return containsAny(s, gcpSignInMarks) || containsAny(s, awsSignInMarks)
}

var (
	gcpSignInMarks = []string{
		"invalid_grant", "invalid_rapt", "reauth related error",
		"could not find default credentials", "oauth2: cannot fetch token",
	}
	awsSignInMarks = []string{
		"ExpiredToken", "RequestExpired", "session has expired",
		"failed to refresh cached credentials", "failed to retrieve credentials",
		"no EC2 IMDS role found", "InvalidClientTokenId", "AuthFailure",
		"SSO session", "token has expired",
	}
)

func cloudErrorHint(s string) string {
	switch {
	case containsAny(s, gcpSignInMarks):
		return "Your Google Cloud sign-in has expired or is missing. Sign in again, then retry:\n  gcloud auth application-default login"
	case containsAny(s, awsSignInMarks):
		return "Your AWS sign-in has expired or is missing. Sign in again (for example 'aws sso login'), then retry."
	case strings.Contains(s, "networkInterfaces[0].network") && strings.Contains(s, "cannot be found"):
		return "This project has no network named 'default'. Name the one to use:\n  --network NAME --subnet NAME   (or gcp.network / gcp.subnetwork in the manifest)"
	case strings.Contains(s, "service control policy"):
		return "Your AWS organization blocks this action here, most often because the region is not one it allows.\nTry the region your organization uses:\n  --region REGION"
	case strings.Contains(s, "amazonaws.com") && strings.Contains(s, "no such host"):
		return "That does not look like an AWS region, or there is no network. Pass a region, for example:\n  --region us-east-1"
	}
	return ""
}

func containsAny(s string, marks []string) bool {
	for _, m := range marks {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

var (
	gcpLocation = regexp.MustCompile(`^[a-z]+-[a-z]+[0-9]+(-[a-z])?$`)    // us-central1 or us-central1-a
	awsLocation = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]+[a-z]?$`) // us-east-1, us-gov-west-1
)

// regionFits reports whether region is written the way prov writes them. The
// two clouds' names never overlap, so this catches a zone saved for one cloud
// being used for the other.
func regionFits(prov, region string) bool {
	switch prov {
	case "gcp":
		return gcpLocation.MatchString(region)
	case "aws":
		return awsLocation.MatchString(region)
	}
	return true
}

// awsConfiguredRegion is the region the user's AWS setup already names: the
// environment first, then the active profile in ~/.aws/config. Empty if none.
func awsConfiguredRegion() string {
	for _, k := range []string{"AWS_REGION", "AWS_DEFAULT_REGION"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	cfg, err := awsconfig.LoadDefaultConfig(context.Background())
	if err != nil {
		return ""
	}
	return cfg.Region
}

// detectProvider picks the cloud for a bare 'runlease up' when exactly one is
// set up on this computer. It returns the provider and why, or "" and what it
// found, so the caller can ask instead of guessing between two.
func detectProvider(ctx context.Context) (prov, why string) {
	gcp := hasGCPSetup()
	aws := hasAWSSetup()
	switch {
	case gcp && !aws:
		return "gcp", "the only cloud signed in on this computer"
	case aws && !gcp:
		return "aws", "the only cloud signed in on this computer"
	case gcp && aws:
		return "", "Both Google Cloud and AWS are set up here, so say which:"
	}
	return "", "No cloud sign-in found. Sign in first ('gcloud auth application-default login' or 'aws sso login'), then:"
}

func hasGCPSetup() bool {
	if os.Getenv("GOOGLE_APPLICATION_CREDENTIALS") != "" {
		return true
	}
	dir := os.Getenv("CLOUDSDK_CONFIG")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return false
		}
		dir = filepath.Join(home, ".config", "gcloud")
	}
	_, err := os.Stat(filepath.Join(dir, "application_default_credentials.json"))
	return err == nil
}

func hasAWSSetup() bool {
	// Every way the AWS SDK is handed credentials through the environment:
	// keys, a profile, a role to assume (CI with OIDC), a container role.
	for _, k := range []string{
		"AWS_ACCESS_KEY_ID", "AWS_PROFILE", "AWS_ROLE_ARN", "AWS_WEB_IDENTITY_TOKEN_FILE",
		"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "AWS_CONTAINER_CREDENTIALS_FULL_URI",
	} {
		if os.Getenv(k) != "" {
			return true
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	for _, f := range []string{"config", "credentials"} {
		if _, err := os.Stat(filepath.Join(home, ".aws", f)); err == nil {
			return true
		}
	}
	return false
}
