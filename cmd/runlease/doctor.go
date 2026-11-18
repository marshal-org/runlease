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
	relayAgent  func(ctx context.Context, relayAddr, want string) (string, error)
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
		relayAgent:  resolveRelayAgent,
		timeout:     doctorCheckTimeout,
	}

	// Worked out first: flags that cannot be placed are an error before any
	// check runs, not a check quietly left out.
	targets, err := doctorTargets(doctorFlags{doctorProvider, doctorRegion, doctorNetwork, doctorSubnet}, cfg)
	if err != nil {
		return err
	}
	checks := doctorCLI(deps)
	checks = append(checks, doctorRelay(ctx, deps)...)
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
	_, err = d.relayAgent(gctx, d.relayAddr, d.agentDigest)
	cancel()
	var mismatch *agentMismatchError
	switch {
	case errors.As(err, &mismatch):
		agent.Status = statusFail
		agent.Detail = fmt.Sprintf("the relay cannot serve the agent this CLI expects (expected SHA-256 %s, relay serves %s), so a machine launched now would refuse it and never connect", d.agentDigest, mismatch.got)
		agent.Fix = "upgrade the relay, or install the runlease release that matches it: https://github.com/marshal-org/runlease/releases\n" +
			"(or set relay.agentSha256 in the manifest to the digest of the agent you trust)"
	case err != nil:
		agent.Status = statusWarn
		agent.Detail = "could not read the agent the relay serves: " + oneLine(err)
		agent.Fix = "runlease doctor   (try again; a machine makes the same check itself at boot)"
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

// doctorFlags is what --provider, --region, --network and --subnet said.
type doctorFlags struct{ provider, region, network, subnet string }

// doctorTargets works out which clouds to check and where. With a provider
// named it is that one. Otherwise it is every applicable cloud (signed in on
// this computer, or the saved default), each in its default place.
//
// --region, --network and --subnet describe one place in one cloud. Without
// --provider they are that cloud's only when exactly one cloud applies;
// otherwise there is no telling whose they are, and the answer is an error
// rather than a flag that is quietly dropped.
func doctorTargets(f doctorFlags, cfg *rlconfig.Config) ([]doctorTarget, error) {
	clouds := applicableClouds(cfg)
	prov := f.provider
	if prov == "" && (f.region != "" || f.network != "" || f.subnet != "") {
		switch len(clouds) {
		case 1:
			prov = clouds[0]
		case 0:
			return nil, fmt.Errorf("--region, --network and --subnet need --provider gcp or --provider aws: no cloud sign-in was found on this computer to apply them to")
		default:
			return nil, fmt.Errorf("--region, --network and --subnet need --provider gcp or --provider aws: both clouds are set up on this computer, and the flags describe a place in one")
		}
	}
	if prov == "aws" && f.network != "" {
		return nil, fmt.Errorf("--network is only supported for GCP")
	}
	if prov != "" {
		t := doctorTarget{provider: prov, region: f.region, regionWhy: "from --region", network: f.network, subnet: f.subnet}
		if t.region == "" {
			t.region, t.regionWhy = defaultRegionFor(prov, cfg)
		}
		return []doctorTarget{t}, nil
	}
	var targets []doctorTarget
	for _, c := range clouds {
		t := doctorTarget{provider: c}
		t.region, t.regionWhy = defaultRegionFor(c, cfg)
		targets = append(targets, t)
	}
	return targets, nil
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
	proj := doctorCheck{ID: id("project"), Title: cloud + " project", Status: statusOK}
	const setProject = "gcloud config set project PROJECT_ID   (or: export GOOGLE_CLOUD_PROJECT=PROJECT_ID)"
	octx, cancel := bounded()
	p, err := open(octx)
	cancel()
	switch {
	case errors.Is(err, provider.ErrNoProject):
		// Google Cloud needs a project before it can be asked anything.
		proj.Status, proj.Detail, proj.Fix = statusFail, "no project is set", setProject
		return []doctorCheck{proj}
	case err != nil:
		cred.Status, cred.Detail, cred.Fix = statusFail, describeCheckErr(err, timeout), credentialsFix(t.provider, err)
		return []doctorCheck{cred}
	}

	pre, ok := p.(provider.Preflighter)
	if !ok {
		cred.Status = statusWarn
		cred.Detail = "runlease has no pre-flight checks for this provider"
		cred.Fix = "runlease up --provider " + t.provider + "   (the launch itself reports what is wrong)"
		return []doctorCheck{cred}
	}
	project := ""
	if n, ok := p.(provider.AccountNamer); ok && t.provider == "gcp" {
		project = n.AccountName(ctx)
	}
	actx, cancel := bounded()
	err = pre.CheckAccess(actx)
	cancel()
	// The provider says which half failed: the sign-in, or the project it was
	// pointed at. The credentials are reported as working only in the second
	// case, and the project as usable only once it has been read.
	projectErr := errors.Is(err, provider.ErrProject) || errors.Is(err, provider.ErrServiceDisabled)
	if err != nil && !projectErr {
		cred.Status, cred.Detail, cred.Fix = statusFail, describeCheckErr(err, timeout), credentialsFix(t.provider, err)
		return []doctorCheck{cred}
	}
	if projectErr {
		cred.Detail = "work"
		if w, ok := p.(provider.IdentityDescriber); ok {
			ictx, cancel := bounded()
			if who := w.Identity(ictx); who != "" {
				cred.Detail = "work, as " + who
			}
			cancel()
		}
		proj.Status, proj.Fix = statusFail, setProject
		proj.Detail = strings.TrimPrefix(describeCheckErr(err, timeout), provider.ErrProject.Error()+": ")
		if errors.Is(err, provider.ErrServiceDisabled) {
			proj.Fix = "gcloud services enable compute.googleapis.com --project " + project
		}
		return []doctorCheck{cred, proj}
	}
	// Before the account is described, so that line names the zone a launch
	// would really use. What came back is judged below, at the region check.
	var (
		pl     *provider.Placement
		perr   error
		placed bool
	)
	if placer, ok := p.(provider.Placer); ok {
		placed = true
		pctx, cancel := bounded()
		pl, perr = placer.ResolvePlacement(pctx, provider.PlacementRequest{
			Network: t.network, Subnet: t.subnet, AnyRegion: regionNotChosen(t.regionWhy)})
		cancel()
	}
	cred.Detail = "work"
	if d, ok := p.(provider.AccountDescriber); ok {
		dctx, cancel := bounded()
		cred.Detail = d.DescribeAccount(dctx)
		cancel()
	}
	checks = append(checks, cred)
	if t.provider == "gcp" {
		proj.Detail = project + " (exists, and this sign-in can use it)"
		checks = append(checks, proj)
	}

	regionFix := doctorRegionFix(t.provider)
	// What a launch fills in itself is not a failure: resolve the zone and
	// network the way 'up' does, and check the result.
	if placed {
		asked := fmt.Sprintf("%s (%s)", t.region, t.regionWhy)
		switch {
		case perr == nil:
			if pl.Region != t.region {
				t.regionWhy = fmt.Sprintf("picked by runlease; you had %s, %s", t.region, t.regionWhy)
			}
			t.region, t.network, t.subnet = pl.Region, pl.Network, pl.Subnet
		case errors.Is(perr, provider.ErrBadRegion):
			what, _ := splitFirstLine(strings.TrimPrefix(perr.Error(), provider.ErrBadRegion.Error()+": "))
			return append(checks, doctorCheck{ID: id("region"), Title: cloud + " " + regionWord(t.provider),
				Status: statusFail, Detail: asked + ": " + what, Fix: regionFix})
		case isSignInError(perr):
			// Reported by the checks below, which name the fix for it.
		case errors.Is(perr, provider.ErrNoNetwork):
			what, fix := splitFirstLine(strings.TrimPrefix(perr.Error(), provider.ErrNoNetwork.Error()+": "))
			if fix == "" {
				fix = networkFix(t.provider)
			}
			return append(checks,
				doctorCheck{ID: id("region"), Title: cloud + " " + regionWord(t.provider), Status: statusOK, Detail: asked},
				doctorCheck{ID: id("network"), Title: cloud + " network", Status: statusFail, Detail: what, Fix: fix})
		}
		// Any other error is the check failing, not the launch: the checks
		// below report it in their own words.
	}

	region := doctorCheck{ID: id("region"), Title: cloud + " " + regionWord(t.provider), Status: statusOK,
		Detail: fmt.Sprintf("%s (%s)", t.region, t.regionWhy)}
	rctx, cancel := bounded()
	err = pre.CheckRegion(rctx)
	cancel()
	if err != nil {
		region.Status = statusFail
		region.Detail = fmt.Sprintf("%s (%s): %s", t.region, t.regionWhy, describeCheckErr(err, timeout))
		region.Fix = regionFix
		// The hint ends by naming the flag; here that becomes the command.
		if hint := cloudErrorHint(err.Error()); hint != "" && !isSignInError(err) {
			region.Fix = strings.TrimSuffix(hint, "\n  --region REGION") + "\n" + regionFix
		} else if t.provider == "aws" {
			// The credentials have just been shown to work, so a refusal here
			// is about this region: most often one the account has not enabled.
			region.Fix = fmt.Sprintf("if %s is a region your account has not enabled: aws account enable-region --region-name %s\n"+
				"or check another: %s", t.region, t.region, regionFix)
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
// credentials.
func credentialsFix(prov string, err error) string {
	if prov == "gcp" {
		return "gcloud auth application-default login"
	}
	if isSignInError(err) {
		return "aws sso login   (or however you sign in to AWS)"
	}
	if hint := cloudErrorHint(err.Error()); hint != "" {
		return hint
	}
	return "aws sts get-caller-identity   (shows what AWS itself says; then sign in again, for example 'aws sso login')"
}

// splitFirstLine separates a message's first line from the lines under it.
func splitFirstLine(s string) (first, rest string) {
	first, rest, _ = strings.Cut(s, "\n")
	return strings.TrimSpace(first), strings.TrimSpace(rest)
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
