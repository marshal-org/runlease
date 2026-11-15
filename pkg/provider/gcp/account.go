package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/oauth2/google"
	"google.golang.org/api/compute/v1"
)

// DescribeAccount says which project, zone and identity this provider acts
// with. The identity is looked up for two seconds at most and left out if
// that fails: this line must never be the reason a launch is slow or fails.
func (p *Provider) DescribeAccount(ctx context.Context) string {
	s := fmt.Sprintf("Google Cloud project %s, zone %s", p.projectID, p.zone)
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if who := adcIdentity(ctx); who != "" {
		s += ", as " + who
	}
	return s
}

// adcIdentity returns the email behind the application default credentials.
func adcIdentity(ctx context.Context) string {
	creds, err := google.FindDefaultCredentials(ctx, compute.ComputeScope)
	if err != nil {
		return ""
	}
	var key struct {
		ClientEmail string `json:"client_email"`
	}
	if json.Unmarshal(creds.JSON, &key) == nil && key.ClientEmail != "" {
		return key.ClientEmail // a service account key names itself
	}
	tok, err := creds.TokenSource.Token()
	if err != nil {
		return ""
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://oauth2.googleapis.com/tokeninfo?access_token="+url.QueryEscape(tok.AccessToken), nil)
	if err != nil {
		return ""
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close() //nolint:errcheck
	var info struct {
		Email string `json:"email"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&info) != nil {
		return ""
	}
	return info.Email
}

// NATAdvice gives the two commands that add Cloud NAT for the subnet's
// network and region, filled in for this project.
func (p *Provider) NATAdvice(network, subnet string) string {
	project, region, _ := p.subnetLocation(network, subnet)
	net := "default"
	if network != "" {
		net = lastSegment(network)
	}
	return fmt.Sprintf("gcloud compute routers create runlease-nat --project %s --network %s --region %s\n"+
		"gcloud compute routers nats create runlease-nat --project %s --router runlease-nat --region %s \\\n"+
		"    --auto-allocate-nat-external-ips --nat-all-subnet-ip-ranges\n"+
		"(Cloud NAT is billed by Google by the hour and by traffic. If you may not change this network,\n"+
		" launch on one that has a NAT gateway with --network NAME --subnet NAME.)",
		project, net, region, project, region)
}
