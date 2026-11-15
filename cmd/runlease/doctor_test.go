package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	rlconfig "github.com/marshal-org/runlease/pkg/config"
	"github.com/marshal-org/runlease/pkg/provider"
)

// fakePreflight is a cloud whose every pre-flight answer is set by the test.
type fakePreflight struct {
	fakeEgressProvider
	accessErr  error
	regionErr  error
	network    string
	networkErr error
	slow       bool // CheckAccess waits for its context to end
	calls      []string
}

func (f *fakePreflight) CheckAccess(ctx context.Context) error {
	f.calls = append(f.calls, "access")
	if f.slow {
		<-ctx.Done()
		return ctx.Err()
	}
	return f.accessErr
}
func (f *fakePreflight) CheckRegion(context.Context) error {
	f.calls = append(f.calls, "region")
	return f.regionErr
}
func (f *fakePreflight) CheckNetwork(context.Context, string, string) (string, error) {
	f.calls = append(f.calls, "network")
	return f.network, f.networkErr
}
func (f *fakePreflight) DescribeAccount(context.Context) string { return "project env-project, as me" }
func (f *fakePreflight) NATAdvice(string, string) string        { return "ADD-A-NAT" }

func checksByID(checks []doctorCheck) map[string]doctorCheck {
	m := map[string]doctorCheck{}
	for _, c := range checks {
		m[c.ID] = c
	}
	return m
}

func ids(checks []doctorCheck) string {
	var s []string
	for _, c := range checks {
		s = append(s, c.ID+"="+c.Status)
	}
	return strings.Join(s, " ")
}

func runCloud(p provider.CloudProvider, openErr error, t doctorTarget) []doctorCheck {
	open := func(context.Context) (provider.CloudProvider, error) { return p, openErr }
	return doctorCloud(context.Background(), t, open, 50*time.Millisecond)
}

var (
	gcpTarget = doctorTarget{provider: "gcp", region: "us-central1-a", regionWhy: "runlease's default"}
	awsTarget = doctorTarget{provider: "aws", region: "us-east-1", regionWhy: "from --region"}
)

func TestDoctorCloud(t *testing.T) {
	scp := errors.New("operation error EC2: DescribeSubnets, https response error StatusCode: 403, RequestID: abc, " +
		"api error UnauthorizedOperation: You are not authorized to perform: ec2:DescribeSubnets with an explicit deny in a service control policy")
	tests := []struct {
		name    string
		target  doctorTarget
		fake    *fakePreflight
		openErr error
		want    string // id=status, in order
		fixes   map[string]string
		details map[string]string
	}{
		{
			name: "all good", target: gcpTarget,
			fake: &fakePreflight{fakeEgressProvider: fakeEgressProvider{has: true}, network: "network default (us-central1)"},
			want: "gcp.project=ok gcp.credentials=ok gcp.region=ok gcp.network=ok gcp.nat=ok",
			details: map[string]string{
				"gcp.project": "env-project", "gcp.credentials": "project env-project, as me",
				"gcp.region": "us-central1-a (runlease's default)", "gcp.network": "network default (us-central1)",
			},
		},
		{
			name: "no NAT is a warning, with the provider's advice", target: gcpTarget,
			fake:  &fakePreflight{network: "network default"},
			want:  "gcp.project=ok gcp.credentials=ok gcp.region=ok gcp.network=ok gcp.nat=warn",
			fixes: map[string]string{"gcp.nat": "launch with --public-ip, or add a NAT gateway:\nADD-A-NAT"},
		},
		{
			name: "a NAT check that cannot be made is a warning", target: gcpTarget,
			fake:  &fakePreflight{fakeEgressProvider: fakeEgressProvider{err: errors.New("403 routers.list")}, network: "network default"},
			want:  "gcp.project=ok gcp.credentials=ok gcp.region=ok gcp.network=ok gcp.nat=warn",
			fixes: map[string]string{"gcp.nat": "launch with --public-ip, or add a NAT gateway"},
		},
		{
			name: "no default network is a failure, and NAT is not asked about", target: gcpTarget,
			fake:    &fakePreflight{networkErr: fmt.Errorf("%w: project p has no network named default", provider.ErrNoNetwork)},
			want:    "gcp.project=ok gcp.credentials=ok gcp.region=ok gcp.network=fail",
			fixes:   map[string]string{"gcp.network": "--network NAME --subnet NAME"},
			details: map[string]string{"gcp.network": "project p has no network named default"},
		},
		{
			name: "a network check that cannot be made is a warning, and NAT is still checked", target: gcpTarget,
			fake: &fakePreflight{fakeEgressProvider: fakeEgressProvider{has: true}, networkErr: errors.New("403")},
			want: "gcp.project=ok gcp.credentials=ok gcp.region=ok gcp.network=warn gcp.nat=ok",
		},
		{
			name: "expired Google sign-in", target: gcpTarget,
			fake:  &fakePreflight{accessErr: errors.New(`oauth2: "invalid_grant" "reauth related error (invalid_rapt)"`)},
			want:  "gcp.project=ok gcp.credentials=fail",
			fixes: map[string]string{"gcp.credentials": "gcloud auth application-default login"},
		},
		{
			name: "Compute Engine API off", target: gcpTarget,
			fake:  &fakePreflight{accessErr: errors.New("googleapi: Error 403: Compute Engine API has not been used in project 1 before or it is disabled")},
			want:  "gcp.project=ok gcp.credentials=fail",
			fixes: map[string]string{"gcp.credentials": "gcloud services enable compute.googleapis.com --project env-project"},
		},
		{
			name: "no project set", target: gcpTarget, fake: &fakePreflight{},
			openErr: errors.New("GCP project not configured: run 'gcloud config set project <project-id>'"),
			want:    "gcp.project=fail",
			fixes:   map[string]string{"gcp.project": "gcloud config set project PROJECT_ID"},
		},
		{
			name: "no credentials file at all", target: gcpTarget, fake: &fakePreflight{},
			openErr: errors.New("creating compute service: google: could not find default credentials"),
			want:    "gcp.credentials=fail",
			fixes:   map[string]string{"gcp.credentials": "gcloud auth application-default login"},
		},
		{
			name: "expired AWS sign-in", target: awsTarget,
			fake:  &fakePreflight{accessErr: errors.New("GetCallerIdentity: failed to refresh cached credentials, the SSO session has expired")},
			want:  "aws.credentials=fail",
			fixes: map[string]string{"aws.credentials": "aws sso login"},
		},
		{
			name: "region denied by the organization: the existing hint", target: awsTarget,
			fake:    &fakePreflight{regionErr: scp},
			want:    "aws.credentials=ok aws.region=fail",
			fixes:   map[string]string{"aws.region": "Your AWS organization blocks this action here"},
			details: map[string]string{"aws.region": "us-east-1 (from --region): UnauthorizedOperation"},
		},
		{
			name: "no default VPC", target: awsTarget,
			fake:  &fakePreflight{networkErr: fmt.Errorf("%w: us-east-1 has no default VPC", provider.ErrNoNetwork)},
			want:  "aws.credentials=ok aws.region=ok aws.network=fail",
			fixes: map[string]string{"aws.network": "--subnet SUBNET_ID"},
		},
		{
			name:   "a zone given to AWS is reported before any call",
			target: doctorTarget{provider: "aws", region: "us-central1-a", regionWhy: "from --region"},
			fake:   &fakePreflight{},
			want:   "aws.region=fail",
			fixes:  map[string]string{"aws.region": "runlease doctor --provider aws --region REGION"},
		},
		{
			name: "a cloud that does not answer is cut off", target: awsTarget,
			fake:    &fakePreflight{slow: true},
			want:    "aws.credentials=fail",
			details: map[string]string{"aws.credentials": "no answer within 50ms"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			checks := runCloud(tc.fake, tc.openErr, tc.target)
			if got := ids(checks); got != tc.want {
				t.Fatalf("checks = %s\n       want %s", got, tc.want)
			}
			by := checksByID(checks)
			for id, want := range tc.fixes {
				if !strings.Contains(by[id].Fix, want) {
					t.Errorf("%s fix = %q, want it to contain %q", id, by[id].Fix, want)
				}
			}
			for id, want := range tc.details {
				if !strings.Contains(by[id].Detail, want) {
					t.Errorf("%s detail = %q, want it to contain %q", id, by[id].Detail, want)
				}
			}
			for _, c := range checks {
				if (c.Status == statusOK) != (c.Fix == "") {
					t.Errorf("%s: status %s with fix %q: a fix goes with warn and fail, and only with them", c.ID, c.Status, c.Fix)
				}
				if c.Title == "" || c.Detail == "" {
					t.Errorf("%s: empty title or detail: %+v", c.ID, c)
				}
			}
		})
	}
}

// Nothing after a failed sign-in can work, so nothing after it is called.
func TestDoctorCloud_stopsCallingAfterCredentialsFail(t *testing.T) {
	f := &fakePreflight{accessErr: errors.New("invalid_grant")}
	runCloud(f, nil, gcpTarget)
	if got := strings.Join(f.calls, ","); got != "access" || f.called {
		t.Errorf("calls = %s, NAT asked = %v; want only the access check", got, f.called)
	}
}

// A provider with no pre-flight checks is said to have none, not passed.
func TestDoctorCloud_providerWithoutPreflight(t *testing.T) {
	checks := runCloud(&fakeDownProvider{}, nil, awsTarget)
	if got := ids(checks); got != "aws.credentials=warn" {
		t.Errorf("checks = %s", got)
	}
}

func relayDeps() doctorDeps {
	return doctorDeps{
		version: "v1.2.3", agentDigest: strings.Repeat("a", 64), relayAddr: "wss://relay.example.com",
		discover: func(context.Context, string) (*relayDiscovery, error) {
			d := &relayDiscovery{Version: "v1.2.3"}
			d.Auth.Required = true
			d.Auth.Google = &struct {
				ClientID     string `json:"clientId"`
				ClientSecret string `json:"clientSecret"`
			}{"client-id", "TOP-SECRET"}
			return d, nil
		},
		account: func(context.Context, string) (*relayAccount, error) {
			a := &relayAccount{Email: "me@example.com", Tier: "free"}
			a.Slots.Used, a.Slots.Max = 1, 2
			return a, nil
		},
		relayAgent: func(context.Context, string) (string, error) { return strings.Repeat("a", 64), nil },
		timeout:    50 * time.Millisecond,
	}
}

func TestDoctorRelay(t *testing.T) {
	tests := []struct {
		name   string
		change func(*doctorDeps)
		want   string
		fixes  map[string]string
		detail map[string]string
	}{
		{
			name: "all good", change: func(*doctorDeps) {},
			want:   "relay.config=ok relay.reachable=ok relay.signin=ok relay.agent=ok",
			detail: map[string]string{"relay.signin": "signed in as me@example.com, tier free, 1 of 2 machines in use", "relay.reachable": "v1.2.3"},
		},
		{
			name: "no relay set", change: func(d *doctorDeps) { d.relayAddr = "" },
			want: "relay.config=warn", fixes: map[string]string{"relay.config": "runlease login"},
		},
		{
			name: "unreachable",
			change: func(d *doctorDeps) {
				d.discover = func(context.Context, string) (*relayDiscovery, error) {
					return nil, errors.New("dial tcp: no such host")
				}
			},
			want: "relay.config=ok relay.reachable=fail", fixes: map[string]string{"relay.reachable": "runlease config set relay.addr"},
		},
		{
			name: "not signed in",
			change: func(d *doctorDeps) {
				d.account = func(context.Context, string) (*relayAccount, error) { return nil, errors.New("unauthorized") }
			},
			want: "relay.config=ok relay.reachable=ok relay.signin=fail relay.agent=ok", fixes: map[string]string{"relay.signin": "runlease login"},
		},
		{
			name: "every slot taken",
			change: func(d *doctorDeps) {
				d.account = func(context.Context, string) (*relayAccount, error) {
					a := &relayAccount{Email: "me@example.com", Tier: "free"}
					a.Slots.Used, a.Slots.Max = 2, 2
					return a, nil
				}
			},
			want: "relay.config=ok relay.reachable=ok relay.signin=warn relay.agent=ok", fixes: map[string]string{"relay.signin": "runlease down NAME"},
		},
		{
			name: "relay with no sign-in",
			change: func(d *doctorDeps) {
				d.discover = func(context.Context, string) (*relayDiscovery, error) { return &relayDiscovery{}, nil }
				d.account = func(context.Context, string) (*relayAccount, error) { panic("must not be asked") }
			},
			want: "relay.config=ok relay.reachable=ok relay.signin=ok relay.agent=ok",
		},
		{
			name: "the relay serves another agent",
			change: func(d *doctorDeps) {
				d.relayAgent = func(context.Context, string) (string, error) { return strings.Repeat("b", 64), nil }
			},
			want:   "relay.config=ok relay.reachable=ok relay.signin=ok relay.agent=fail",
			detail: map[string]string{"relay.agent": strings.Repeat("b", 64)},
		},
		{
			name: "the agent cannot be read",
			change: func(d *doctorDeps) {
				d.relayAgent = func(context.Context, string) (string, error) { return "", errors.New("GET /agent: 503") }
			},
			want: "relay.config=ok relay.reachable=ok relay.signin=ok relay.agent=warn",
		},
		{
			name: "a source build has nothing to compare",
			change: func(d *doctorDeps) {
				d.agentDigest = ""
				d.relayAgent = func(context.Context, string) (string, error) { panic("must not be asked") }
			},
			want: "relay.config=ok relay.reachable=ok relay.signin=ok",
		},
		{
			name: "unreadable config", change: func(d *doctorDeps) { d.configErr = errors.New("parsing config: yaml: line 1") },
			want: "relay.config=fail",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("RUNLEASE_HOME", t.TempDir())
			d := relayDeps()
			tc.change(&d)
			checks := doctorRelay(context.Background(), d)
			if got := ids(checks); got != tc.want {
				t.Fatalf("checks = %s\n       want %s", got, tc.want)
			}
			by := checksByID(checks)
			for id, want := range tc.fixes {
				if !strings.Contains(by[id].Fix, want) {
					t.Errorf("%s fix = %q, want it to contain %q", id, by[id].Fix, want)
				}
			}
			for id, want := range tc.detail {
				if !strings.Contains(by[id].Detail, want) {
					t.Errorf("%s detail = %q, want it to contain %q", id, by[id].Detail, want)
				}
			}
			// The discovery document holds the sign-in client's secret.
			if b, _ := json.Marshal(checks); bytes.Contains(b, []byte("TOP-SECRET")) || bytes.Contains(b, []byte("client-id")) {
				t.Errorf("the report leaks the relay's sign-in client: %s", b)
			}
		})
	}
}

func TestDoctorCLI(t *testing.T) {
	release := doctorCLI(doctorDeps{version: "v1.2.3", agentDigest: strings.Repeat("a", 64)})
	if got := ids(release); got != "cli.version=ok cli.agent=ok" || !strings.HasPrefix(release[0].Detail, "v1.2.3 (") {
		t.Errorf("release build: %s %q", got, release[0].Detail)
	}
	source := doctorCLI(doctorDeps{version: "dev"})
	if got := ids(source); got != "cli.version=ok cli.agent=warn" || source[1].Fix == "" {
		t.Errorf("source build: %s, fix %q", got, source[1].Fix)
	}
}

func TestDoctorTargets(t *testing.T) {
	orig := configuredAWSRegion
	t.Cleanup(func() { configuredAWSRegion = orig })
	configuredAWSRegion = func(context.Context) string { return "eu-north-1" }
	saved := &rlconfig.Config{Defaults: rlconfig.InstanceConfig{Provider: "gcp", Region: "europe-west1-b"}}
	show := func(ts []doctorTarget) string {
		var s []string
		for _, t := range ts {
			s = append(s, fmt.Sprintf("%s/%s/%s/%s", t.provider, t.region, t.network, t.subnet))
		}
		return strings.Join(s, " ")
	}
	tests := []struct {
		name                          string
		prov, region, network, subnet string
		cfg                           *rlconfig.Config
		gcp, aws                      bool
		want                          string
	}{
		{name: "nothing set up", want: ""},
		{name: "each cloud set up, in its default place", gcp: true, aws: true, want: "gcp/us-central1-a// aws/eu-north-1//"},
		{name: "the saved region goes to the cloud it fits", cfg: saved, gcp: true, aws: true, want: "gcp/europe-west1-b// aws/eu-north-1//"},
		{name: "a saved provider counts with no sign-in found", cfg: saved, want: "gcp/europe-west1-b//"},
		{name: "a provider named: only it, with the flags", prov: "aws", region: "us-east-1", subnet: "subnet-1", gcp: true, aws: true, want: "aws/us-east-1//subnet-1"},
		{name: "a region with no provider goes to the cloud it fits", region: "us-west-2", gcp: true, aws: true, want: "gcp/us-central1-a// aws/us-west-2//"},
		{name: "network and a named subnetwork are Google Cloud's", network: "vpc", subnet: "apps", gcp: true, aws: true, want: "gcp/us-central1-a/vpc/apps aws/eu-north-1//"},
		{name: "a subnet ID is AWS's", subnet: "subnet-9", gcp: true, aws: true, want: "gcp/us-central1-a// aws/eu-north-1//subnet-9"},
		{name: "a misfit region is kept for the named provider, to be reported", prov: "aws", region: "us-central1-a", want: "aws/us-central1-a//"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := show(doctorTargets(tc.prov, tc.region, tc.network, tc.subnet, tc.cfg, tc.gcp, tc.aws)); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// The JSON shape is public: ok, and checks of {id, title, status, detail, fix}.
func TestDoctorReport_JSON(t *testing.T) {
	r := newDoctorReport([]doctorCheck{
		{ID: "cli.version", Title: "CLI version", Status: statusOK, Detail: "dev"},
		{ID: "gcp.nat", Title: "Google Cloud NAT gateway", Status: statusWarn, Detail: "none", Fix: "launch with --public-ip"},
	})
	if !r.OK {
		t.Error("a warning must not make the report fail")
	}
	var buf bytes.Buffer
	if err := writeJSON(&buf, r); err != nil {
		t.Fatal(err)
	}
	var got struct {
		OK     *bool               `json:"ok"`
		Checks []map[string]string `json:"checks"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("%v\n%s", err, buf.String())
	}
	if got.OK == nil || !*got.OK || len(got.Checks) != 2 {
		t.Fatalf("got %+v\n%s", got, buf.String())
	}
	for _, c := range got.Checks {
		for _, key := range []string{"id", "title", "status", "detail", "fix"} {
			if _, ok := c[key]; !ok {
				t.Errorf("check %s has no %q key: every check carries all five", c["id"], key)
			}
		}
	}
	if got.Checks[1]["status"] != "warn" || got.Checks[1]["fix"] != "launch with --public-ip" {
		t.Errorf("warn row = %v", got.Checks[1])
	}

	failed := newDoctorReport([]doctorCheck{{ID: "aws.region", Status: statusFail}})
	if failed.OK {
		t.Error("a failed check must make ok false")
	}
	buf.Reset()
	_ = writeJSON(&buf, newDoctorReport(nil))
	if !strings.Contains(buf.String(), `"checks": []`) {
		t.Errorf("no checks must be an empty array, not null: %s", buf.String())
	}
}

func TestPrintDoctor(t *testing.T) {
	var out bytes.Buffer
	printDoctor(&out, newDoctorReport([]doctorCheck{
		{ID: "cli.version", Title: "CLI version", Status: statusOK, Detail: "dev"},
		{ID: "gcp.nat", Title: "Google Cloud NAT gateway", Status: statusWarn, Detail: "none found", Fix: "launch with --public-ip:\nline two"},
		{ID: "aws.region", Title: "AWS region", Status: statusFail, Detail: "denied", Fix: "--region REGION"},
	}))
	want := "ok    CLI version: dev\n" +
		"warn  Google Cloud NAT gateway: none found\n" +
		"      fix: launch with --public-ip:\n" +
		"           line two\n" +
		"fail  AWS region: denied\n" +
		"      fix: --region REGION\n" +
		"\n1 check failed, 1 warning. Nothing was created or changed.\n"
	if out.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", out.String(), want)
	}
}
