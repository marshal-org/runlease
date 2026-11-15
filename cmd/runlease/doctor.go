package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	rlconfig "github.com/marshal-org/runlease/pkg/config"
	"github.com/marshal-org/runlease/pkg/provider"
)

// doctorCheckTimeout bounds every call doctor makes to a cloud or the relay,
// so a slow or unreachable service delays the report and never hangs it.
const doctorCheckTimeout = 10 * time.Second

const (
	statusOK   = "ok"
	statusWarn = "warn"
	statusFail = "fail"
)

var (
	doctorProvider string
	doctorRegion   string
	doctorNetwork  string
	doctorSubnet   string
)

func init() {
	doctorCmd.Flags().StringVarP(&doctorProvider, "provider", "p", "", "check only this cloud: gcp or aws (default: every cloud that looks set up)")
	doctorCmd.Flags().StringVarP(&doctorRegion, "region", "r", "", "region/zone to check (default: the one 'runlease up' would use)")
	doctorCmd.Flags().StringVar(&doctorNetwork, "network", "", "GCP VPC network to check (default: the 'default' network)")
	doctorCmd.Flags().StringVar(&doctorSubnet, "subnet", "", "subnetwork (GCP) or subnet ID (AWS) to check")
	rootCmd.AddCommand(doctorCmd)
}

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check your sign-ins, project and network, and print the fix for what is wrong",
	Long: `Check everything a launch depends on, without creating anything.

It looks at this CLI, the relay (configured, reachable, signed in, serving the
agent this CLI expects) and each cloud that is set up on this computer
(credentials, project or account, region, network, NAT gateway). Every check
runs; none stops the others. A check that needs attention prints the command
that fixes it.

Exits 1 if any check failed. Warnings do not fail.

  runlease doctor
  runlease doctor --provider aws --region eu-north-1
  runlease doctor --provider gcp --network my-vpc --subnet my-subnet
  runlease doctor --output json`,
	Args: cobra.NoArgs,
	RunE: runDoctor,
}

// doctorCheck is one line of the report. The JSON shape is public: ids are
// stable, and status is "ok", "warn" or "fail".
type doctorCheck struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Fix    string `json:"fix"` // empty when status is "ok"
}

type doctorReport struct {
	OK     bool          `json:"ok"` // false if any check failed; warnings do not count
	Checks []doctorCheck `json:"checks"`
}

func newDoctorReport(checks []doctorCheck) doctorReport {
	r := doctorReport{OK: true, Checks: checks}
	if r.Checks == nil {
		r.Checks = []doctorCheck{}
	}
	for _, c := range checks {
		if c.Status == statusFail {
			r.OK = false
		}
	}
	return r
}

func (r doctorReport) count(status string) (n int) {
	for _, c := range r.Checks {
		if c.Status == status {
			n++
		}
	}
	return n
}

// doctorDeps is what the CLI and relay checks read, gathered in one place so
// a test can stand in for the relay.
type doctorDeps struct {
	version     string
	agentDigest string // "" in a source build
	relayAddr   string
	configErr   error // the config file could not be read
	discover    func(ctx context.Context, relayAddr string) (*relayDiscovery, error)
	account     func(ctx context.Context, relayAddr string) (*relayAccount, error)
	relayAgent  func(ctx context.Context, relayAddr string) (string, error)
	timeout     time.Duration
}

// doctorTarget is one cloud to check, and where in it.
type doctorTarget struct {
	provider  string
	region    string // AWS region or GCP zone
	regionWhy string // where the region came from, for the report
	network   string // GCP only
	subnet    string
}

func runDoctor(cmd *cobra.Command, _ []string) error {
	if doctorProvider != "" && doctorProvider != "gcp" && doctorProvider != "aws" {
		return fmt.Errorf("unknown provider %q — supported: aws, gcp", doctorProvider)
	}
	if doctorNetwork != "" && doctorProvider == "aws" {
		return fmt.Errorf("--network is only supported for GCP")
	}
	ctx := cmd.Context()
	cfg, cfgErr := rlconfig.Load(defaultConfigPath())
	if cfg == nil {
		cfg = &rlconfig.Config{}
	}
	digest, _ := resolveAgentSHA256("", agentSHA256)
	deps := doctorDeps{
		version:     version,
		agentDigest: digest,
		relayAddr:   cfg.Relay.Addr,
		configErr:   cfgErr,
		discover:    fetchDiscovery,
		account:     fetchAccount,
		relayAgent:  relayAgentDigest,
		timeout:     doctorCheckTimeout,
	}

	checks := doctorCLI(deps)
	checks = append(checks, doctorRelay(ctx, deps)...)
	targets := doctorTargets(doctorProvider, doctorRegion, doctorNetwork, doctorSubnet, cfg, hasGCPSetup(), hasAWSSetup())
	if len(targets) == 0 {
		checks = append(checks, doctorCheck{
			ID: "cloud", Title: "Cloud sign-in", Status: statusFail,
			Detail: "no Google Cloud or AWS sign-in found on this computer",
			Fix:    "gcloud auth application-default login   (Google Cloud)\naws sso login                           (AWS)",
		})
	}
	for _, t := range targets {
		open := func(ctx context.Context) (provider.CloudProvider, error) {
			return newProvider(ctx, provider.ProviderType(t.provider), t.region, "")
		}
		checks = append(checks, doctorCloud(ctx, t, open, deps.timeout)...)
	}

	report := newDoctorReport(checks)
	out := cmd.OutOrStdout()
	if jsonOutput() {
		if err := writeJSON(out, report); err != nil {
			return err
		}
	} else {
		printDoctor(out, report)
	}
	if n := report.count(statusFail); n > 0 {
		return fmt.Errorf("%s failed", plural(n, "check"))
	}
	return nil
}

func printDoctor(out io.Writer, r doctorReport) {
	for _, c := range r.Checks {
		fmt.Fprintf(out, "%-4s  %s: %s\n", c.Status, c.Title, indentRest(c.Detail, "      "))
		if c.Fix != "" {
			fmt.Fprintf(out, "      fix: %s\n", indentRest(c.Fix, "           "))
		}
	}
	fails, warns := r.count(statusFail), r.count(statusWarn)
	switch {
	case fails == 0 && warns == 0:
		fmt.Fprintf(out, "\nAll %d checks passed.\n", len(r.Checks))
	case fails == 0:
		fmt.Fprintf(out, "\nNo failures, %s. Nothing was created or changed.\n", plural(warns, "warning"))
	default:
		fmt.Fprintf(out, "\n%s failed, %s. Nothing was created or changed.\n", plural(fails, "check"), plural(warns, "warning"))
	}
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// indentRest puts prefix in front of every line of s but the first.
func indentRest(s, prefix string) string {
	return strings.ReplaceAll(s, "\n", "\n"+prefix)
}

// oneLine flattens an SDK error, which may run over many lines, into one line
// short enough for a report.
func oneLine(err error) string {
	s := strings.Join(strings.Fields(err.Error()), " ")
	// An AWS SDK error opens with the operation, status code and request ID.
	// What went wrong comes after "api error".
	if i := strings.Index(s, "api error "); i >= 0 {
		s = s[i+len("api error "):]
	}
	const max = 400
	if len(s) > max {
		s = s[:max] + "..."
	}
	return s
}

// doctorCLI reports the version, and whether this build can verify the agent
// a relay serves.
func doctorCLI(d doctorDeps) []doctorCheck {
	checks := []doctorCheck{{
		ID: "cli.version", Title: "CLI version", Status: statusOK,
		Detail: fmt.Sprintf("%s (%s/%s)", d.version, runtime.GOOS, runtime.GOARCH),
	}}
	agent := doctorCheck{ID: "cli.agent", Title: "Agent digest", Status: statusOK}
	if d.agentDigest == "" {
		agent.Status = statusWarn
		agent.Detail = "this build carries no agent digest (a source build), so the agent a relay serves is not verified before a machine runs it"
		agent.Fix = "install a release build: https://github.com/marshal-org/runlease/releases\n" +
			"(or set relay.agentSha256 in the manifest to the digest of the agent you trust)"
	} else {
		agent.Detail = "machines verify the relay's agent against SHA-256 " + d.agentDigest
	}
	return append(checks, agent)
}

// doctorRelay checks the relay: set, reachable, signed in, and serving the
// agent this CLI expects. A check that cannot be made because the one before
// it failed is left out: it would only repeat that failure.
func doctorRelay(ctx context.Context, d doctorDeps) []doctorCheck {
	conf := doctorCheck{ID: "relay.config", Title: "Relay", Status: statusOK, Detail: d.relayAddr}
	switch {
	case d.configErr != nil:
		conf.Status = statusFail
		conf.Detail = oneLine(d.configErr)
		conf.Fix = "repair or remove " + defaultConfigPath() + ", then: runlease login"
		return []doctorCheck{conf}
	case d.relayAddr == "":
		conf.Status = statusWarn
		conf.Detail = "no relay chosen, so machines can only be reached on a public IP"
		conf.Fix = "runlease login"
		return []doctorCheck{conf}
	}
	checks := []doctorCheck{conf}

	reach := doctorCheck{ID: "relay.reachable", Title: "Relay reachable", Status: statusOK}
	rctx, cancel := context.WithTimeout(ctx, d.timeout)
	disc, err := d.discover(rctx, d.relayAddr)
	cancel()
	if err != nil {
		reach.Status = statusFail
		reach.Detail = oneLine(err)
		reach.Fix = fmt.Sprintf("check your network and the address, or choose another relay:\n"+
			"runlease config set relay.addr wss://YOUR-RELAY   (the free public relay is %s)", publicRelay)
		return append(checks, reach)
	}
	// Only the version is shown. The rest of the document holds the sign-in
	// client's ID and secret, which do not belong in a report.
	reach.Detail = "answers"
	if disc.Version != "" {
		reach.Detail = "answers, relay version " + disc.Version
	}
	checks = append(checks, reach)

	sign := doctorCheck{ID: "relay.signin", Title: "Relay sign-in", Status: statusOK}
	if !disc.Auth.Required {
		sign.Detail = "this relay does not require sign-in"
	} else {
		actx, cancel := context.WithTimeout(ctx, d.timeout)
		a, err := d.account(actx, d.relayAddr)
		cancel()
		switch {
		case err != nil:
			sign.Status = statusFail
			sign.Detail = "not signed in: " + oneLine(err)
			sign.Fix = "runlease login"
		default:
			sign.Detail = describeRelayAccount(a)
			if a.Slots.Max > 0 && a.Slots.Used >= a.Slots.Max {
				sign.Status = statusWarn
				sign.Detail += ". Every slot is taken, so a launch now would be refused"
				sign.Fix = "runlease list   (then 'runlease down NAME' for one you no longer need)"
			}
		}
	}
	checks = append(checks, sign)

	if d.agentDigest == "" {
		return checks // nothing to compare with: cli.agent already says so
	}
	agent := doctorCheck{ID: "relay.agent", Title: "Relay agent", Status: statusOK}
	gctx, cancel := context.WithTimeout(ctx, d.timeout)
	got, err := d.relayAgent(gctx, d.relayAddr)
	cancel()
	switch mismatch := relayAgentMismatch(d.relayAddr, d.agentDigest, got); {
	case err != nil:
		agent.Status = statusWarn
		agent.Detail = "could not read the agent the relay serves: " + oneLine(err)
		agent.Fix = "runlease doctor   (try again; a machine makes the same check itself at boot)"
	case mismatch != nil:
		agent.Status = statusFail
		agent.Detail = fmt.Sprintf("the relay serves an agent this CLI does not expect (expected SHA-256 %s, relay serves %s), so a machine launched now would refuse it and never connect", d.agentDigest, got)
		agent.Fix = "install the runlease release that matches the relay: https://github.com/marshal-org/runlease/releases\n" +
			"(or set relay.agentSha256 in the manifest to the digest of the agent you trust)"
	default:
		agent.Detail = "the relay serves the agent this CLI expects"
	}
	return append(checks, agent)
}

func describeRelayAccount(a *relayAccount) string {
	s := "signed in"
	if a.Email != "" {
		s += " as " + a.Email
	}
	if a.Tier != "" {
		s += ", tier " + a.Tier
	}
	switch {
	case a.Slots.Max > 0:
		s += fmt.Sprintf(", %d of %d machines in use", a.Slots.Used, a.Slots.Max)
	default:
		s += fmt.Sprintf(", %d machines in use, no limit", a.Slots.Used)
	}
	return s
}

// doctorTargets works out which clouds to check and where. With a provider
// named it is that one. Otherwise it is every cloud that looks set up on this
// computer, plus the saved default provider. A --region that does not fit a
// cloud it was not explicitly paired with is left to that cloud's default.
func doctorTargets(prov, region, network, subnet string, cfg *rlconfig.Config, gcpSetup, awsSetup bool) []doctorTarget {
	var provs []string
	switch {
	case prov != "":
		provs = []string{prov}
	default:
		saved := ""
		if cfg != nil {
			saved = cfg.Defaults.Provider
		}
		if gcpSetup || saved == "gcp" {
			provs = append(provs, "gcp")
		}
		if awsSetup || saved == "aws" {
			provs = append(provs, "aws")
		}
	}
	var targets []doctorTarget
	for _, p := range provs {
		t := doctorTarget{provider: p}
		switch {
		case region != "" && (prov != "" || regionFits(p, region)):
			t.region, t.regionWhy = region, "from --region"
		default:
			t.region, t.regionWhy = defaultRegionFor(p, cfg)
		}
		// A subnet ID is AWS's; a name or a self-link is Google Cloud's. With
		// the provider named, the flags are that provider's.
		awsSubnet := strings.HasPrefix(subnet, "subnet-")
		if prov != "" || (p == "aws") == awsSubnet {
			t.subnet = subnet
		}
		if p == "gcp" {
			t.network = network
		}
		targets = append(targets, t)
	}
	return targets
}

// doctorCloud runs the checks for one cloud. open builds the provider; every
// call after it is read-only and bounded by timeout. A check whose answer
// could only repeat an earlier failure (the network, once the credentials are
// known not to work) is left out.
func doctorCloud(ctx context.Context, t doctorTarget, open func(context.Context) (provider.CloudProvider, error), timeout time.Duration) []doctorCheck {
	id := func(name string) string { return t.provider + "." + name }
	cloud := cloudName(t.provider)
	bounded := func() (context.Context, context.CancelFunc) { return context.WithTimeout(ctx, timeout) }
	var checks []doctorCheck

	// A region written the other cloud's way would send every call below to
	// a host that does not exist, so it is the one thing reported alone.
	if !regionFits(t.provider, t.region) {
		return []doctorCheck{{
			ID: id("region"), Title: cloud + " " + regionWord(t.provider), Status: statusFail,
			Detail: fmt.Sprintf("%q does not fit %s: %s", t.region, t.provider, regionExample(t.provider)),
			Fix:    doctorRegionFix(t.provider),
		}}
	}

	cred := doctorCheck{ID: id("credentials"), Title: cloud + " credentials", Status: statusOK}
	octx, cancel := bounded()
	p, err := open(octx)
	cancel()
	if err != nil {
		// Google Cloud needs a project before it can be asked anything, and
		// finding none is not a sign-in problem.
		if t.provider == "gcp" && !isSignInError(err) && strings.Contains(strings.ToLower(err.Error()), "project") {
			return append(checks, doctorCheck{
				ID: id("project"), Title: cloud + " project", Status: statusFail,
				Detail: "no project is set",
				Fix:    "gcloud config set project PROJECT_ID   (or: export GOOGLE_CLOUD_PROJECT=PROJECT_ID)",
			})
		}
		cred.Status, cred.Detail, cred.Fix = statusFail, oneLine(err), credentialsFix(t.provider, "", err)
		return append(checks, cred)
	}
	project := ""
	if pp, ok := p.(interface{ ProjectID() string }); ok && t.provider == "gcp" {
		project = pp.ProjectID()
		checks = append(checks, doctorCheck{ID: id("project"), Title: cloud + " project", Status: statusOK, Detail: project})
	}

	pre, ok := p.(provider.Preflighter)
	if !ok {
		cred.Status = statusWarn
		cred.Detail = "runlease has no pre-flight checks for this provider"
		cred.Fix = "runlease up --provider " + t.provider + "   (the launch itself reports what is wrong)"
		return append(checks, cred)
	}
	actx, cancel := bounded()
	err = pre.CheckAccess(actx)
	cancel()
	if err != nil {
		cred.Status, cred.Detail, cred.Fix = statusFail, describeCheckErr(err, timeout), credentialsFix(t.provider, project, err)
		return append(checks, cred)
	}
	cred.Detail = "work"
	if d, ok := p.(provider.AccountDescriber); ok {
		dctx, cancel := bounded()
		cred.Detail = d.DescribeAccount(dctx)
		cancel()
	}
	checks = append(checks, cred)

	region := doctorCheck{ID: id("region"), Title: cloud + " " + regionWord(t.provider), Status: statusOK,
		Detail: fmt.Sprintf("%s (%s)", t.region, t.regionWhy)}
	regionFix := doctorRegionFix(t.provider)
	rctx, cancel := bounded()
	err = pre.CheckRegion(rctx)
	cancel()
	if err != nil {
		region.Status = statusFail
		region.Detail = fmt.Sprintf("%s (%s): %s", t.region, t.regionWhy, describeCheckErr(err, timeout))
		region.Fix = regionFix
		// The hint ends by naming the flag; here that becomes the command.
		if hint := cloudErrorHint(err.Error()); hint != "" {
			region.Fix = strings.TrimSuffix(hint, "\n  --region REGION") + "\n" + regionFix
		}
		return append(checks, region)
	}
	checks = append(checks, region)

	network := doctorCheck{ID: id("network"), Title: cloud + " network", Status: statusOK}
	nctx, cancel := bounded()
	name, err := pre.CheckNetwork(nctx, t.network, t.subnet)
	cancel()
	switch {
	case errors.Is(err, provider.ErrNoNetwork):
		network.Status = statusFail
		network.Detail = strings.TrimPrefix(oneLine(err), provider.ErrNoNetwork.Error()+": ")
		network.Fix = networkFix(t.provider)
		return append(checks, network)
	case err != nil:
		network.Status = statusWarn
		network.Detail = "could not check: " + describeCheckErr(err, timeout)
		network.Fix = networkFix(t.provider)
	default:
		network.Detail = name
	}
	checks = append(checks, network)

	return append(checks, doctorNAT(ctx, t, p, timeout))
}

// doctorNAT reports whether a machine with no public IP would have a way out.
// None is a warning, not a failure: --public-ip launches fine without one.
func doctorNAT(ctx context.Context, t doctorTarget, p provider.CloudProvider, timeout time.Duration) doctorCheck {
	nat := doctorCheck{ID: t.provider + ".nat", Title: cloudName(t.provider) + " NAT gateway", Status: statusOK}
	fix := "launch with --public-ip, or add a NAT gateway"
	checker, ok := p.(provider.EgressChecker)
	if !ok {
		nat.Status = statusWarn
		nat.Detail = "runlease cannot check this provider for a NAT gateway"
		nat.Fix = fix
		return nat
	}
	ectx, cancel := context.WithTimeout(ctx, timeout)
	has, err := checker.HasPrivateEgress(ectx, t.network, t.subnet)
	cancel()
	switch {
	case err != nil:
		nat.Status = statusWarn
		nat.Detail = "could not check: " + describeCheckErr(err, timeout)
		nat.Fix = fix
	case !has:
		nat.Status = statusWarn
		nat.Detail = "none found for this subnet, so a machine with no public IP could not reach the relay"
		nat.Fix = fix
		if a, ok := p.(provider.EgressAdviser); ok {
			nat.Fix += ":\n" + a.NATAdvice(t.network, t.subnet)
		}
	default:
		nat.Detail = "found: machines reach the relay with no public IP"
	}
	return nat
}

// describeCheckErr is the error on one line, or a plain sentence when the
// check ran out of time.
func describeCheckErr(err error, timeout time.Duration) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Sprintf("no answer within %s", timeout)
	}
	return oneLine(err)
}

// credentialsFix is the command to run when a cloud will not accept the
// credentials, or will not let them read the project.
func credentialsFix(prov, project string, err error) string {
	if isSignInError(err) {
		if prov == "gcp" {
			return "gcloud auth application-default login"
		}
		return "aws sso login   (or however you sign in to AWS)"
	}
	if hint := cloudErrorHint(err.Error()); hint != "" {
		return hint
	}
	if prov == "gcp" {
		if project == "" {
			project = "PROJECT_ID"
		}
		if s := err.Error(); strings.Contains(s, "has not been used") || strings.Contains(s, "is disabled") {
			return "gcloud services enable compute.googleapis.com --project " + project
		}
		return "gcloud auth application-default login   (sign in as an account that can use Compute Engine in " + project + ")\n" +
			"or choose another project: export GOOGLE_CLOUD_PROJECT=PROJECT_ID"
	}
	return "aws sts get-caller-identity   (shows what AWS itself says; then sign in again, for example 'aws sso login')"
}

func doctorRegionFix(prov string) string {
	return fmt.Sprintf("runlease doctor --provider %s --region %s   (then launch with the same --region, or save it: runlease configure)",
		prov, strings.ToUpper(regionWord(prov)))
}

func networkFix(prov string) string {
	if prov == "gcp" {
		return "name the network to launch on:\nrunlease up --provider gcp --network NAME --subnet NAME   (or gcp.network / gcp.subnetwork in the manifest)"
	}
	return "name the subnet to launch in:\nrunlease up --provider aws --subnet SUBNET_ID   (or aws.subnetID in the manifest)"
}

func cloudName(prov string) string {
	switch prov {
	case "gcp":
		return "Google Cloud"
	case "aws":
		return "AWS"
	}
	return prov
}

func regionWord(prov string) string {
	if prov == "gcp" {
		return "zone"
	}
	return "region"
}
