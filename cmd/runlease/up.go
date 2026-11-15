package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
	"google.golang.org/api/idtoken"

	rlconfig "github.com/marshal-org/runlease/pkg/config"
	"github.com/marshal-org/runlease/pkg/credential"
	"github.com/marshal-org/runlease/pkg/instanceclass"
	"github.com/marshal-org/runlease/pkg/manifest"
	"github.com/marshal-org/runlease/pkg/pricing"
	"github.com/marshal-org/runlease/pkg/provider"
	awsprovider "github.com/marshal-org/runlease/pkg/provider/aws"
	gcpprovider "github.com/marshal-org/runlease/pkg/provider/gcp"
	"github.com/marshal-org/runlease/pkg/registry"
	"github.com/marshal-org/runlease/pkg/relay"
	"github.com/marshal-org/runlease/pkg/userdata"
)

var (
	upPublicIP           bool
	manifestPath         string
	upTimeout            time.Duration
	detach               bool
	attach               bool
	quickProvider        string
	quickRegion          string
	quickSize            string
	quickDiskSize        int
	quickImage           string
	quickRelay           string
	quickTTL             string
	quickName            string
	quickSelfTerminate   bool
	quickServiceAccount  string
	quickInstanceProfile string
	quickNetwork         string
	quickSubnet          string
)

func init() {
	upCmd.Flags().StringVarP(&manifestPath, "manifest", "m", "", "path to manifest YAML")
	upCmd.Flags().DurationVar(&upTimeout, "timeout", 5*time.Minute, "timeout waiting for instance to become ready")
	upCmd.Flags().BoolVarP(&detach, "detach", "d", false, "exit after provisioning without waiting for SSH")
	upCmd.Flags().BoolVar(&attach, "attach", false, "after provisioning, tail setup logs and apply manifest.forwards (blocks until Ctrl+C)")
	upCmd.Flags().BoolVar(&upPublicIP, "public-ip", false, "give the instance a public IP (default: only when no relay is set); overrides the manifest")
	upCmd.Flags().StringVarP(&quickProvider, "provider", "p", "", "provider: aws or gcp (quick mode — no manifest needed)")
	upCmd.Flags().StringVarP(&quickRegion, "region", "r", "", "region/zone (quick mode; defaults: us-east-1 for aws, us-central1-a for gcp)")
	upCmd.Flags().StringVarP(&quickSize, "size", "s", "compute.small", "instance class (quick mode)")
	upCmd.Flags().IntVar(&quickDiskSize, "disk-size", 0, "boot disk size in GB (quick mode; 0 = image default)")
	upCmd.Flags().StringVar(&quickImage, "image", "", "base image ID (quick mode; GCP defaults to debian-12)")
	upCmd.Flags().StringVar(&quickRelay, "relay", "", "relay address, e.g. wss://relay.example.com (quick mode)")
	upCmd.Flags().StringVar(&quickTTL, "ttl", "2h", "instance TTL (quick mode)")
	upCmd.Flags().StringVarP(&quickName, "name", "n", "", "instance name (quick mode; auto-generated if empty)")
	upCmd.Flags().BoolVar(&quickSelfTerminate, "self-terminate", true, "allow the instance to delete itself (required for TTL to work)")
	upCmd.Flags().StringVar(&quickServiceAccount, "service-account", "", "GCP service account email to attach (quick mode)")
	upCmd.Flags().StringVar(&quickInstanceProfile, "instance-profile", "", "AWS IAM instance profile ARN to attach (quick mode)")
	upCmd.Flags().StringVar(&quickNetwork, "network", "", "GCP VPC network name or self-link (quick mode)")
	upCmd.Flags().StringVar(&quickSubnet, "subnet", "", "subnetwork (GCP) or subnet ID (AWS) name/self-link (quick mode; required for custom-mode GCP VPCs)")
	rootCmd.AddCommand(upCmd)
}

var upCmd = &cobra.Command{
	Use:   "up",
	Short: "Launch a new spot instance from a manifest or quick flags",
	Long: `Launch a new spot instance.

With a manifest file:
  runlease up -m manifest.yaml

Quick mode (no manifest needed):
  runlease up --provider gcp
  runlease up --provider aws --region us-west-2 --size compute.large --image ami-xxxxx
  runlease up --provider gcp --relay wss://relay.example.com`,
	RunE: runUp,
}

func runUp(cmd *cobra.Command, _ []string) error {
	var m *manifest.Manifest
	var err error
	switch {
	case manifestPath != "":
		m, err = manifest.Load(manifestPath)
		if err != nil {
			return err
		}
	case quickProvider != "":
		m, err = buildQuickManifest()
		if err != nil {
			return err
		}
	default:
		// 1. Look for runlease.yaml walking up from cwd. This lets `runlease up`
		//    "just work" inside a project directory the same way `git status` does.
		if discovered, derr := discoverProjectManifest(); derr == nil && discovered != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "📄 Using discovered manifest: %s\n", discovered)
			m, err = manifest.Load(discovered)
			if err != nil {
				return err
			}
		} else {
			// 2. Fall back to config default provider for quick mode.
			cfg, _ := rlconfig.Load(defaultConfigPath())
			if cfg != nil && cfg.Defaults.Provider != "" {
				quickProvider = cfg.Defaults.Provider
				m, err = buildQuickManifest()
				if err != nil {
					return err
				}
			} else {
				return fmt.Errorf("provide --manifest/-m <file>, --provider/-p <aws|gcp>, or set a default with: runlease configure")
			}
		}
	}

	if cmd.Flags().Changed("public-ip") {
		m.PublicIP = &upPublicIP
	}

	if m.Provider == "aws" && m.BaseImage == "" {
		fmt.Fprintf(cmd.OutOrStdout(), "🔍 Resolving latest Amazon Linux 2023 AMI for %s...\n", m.Region)
		ami, err := awsprovider.ResolveAL2023AMI(cmd.Context(), m.Region)
		if err != nil {
			return fmt.Errorf("auto-resolving AMI: %w", err)
		}
		m.BaseImage = ami
		fmt.Fprintf(cmd.OutOrStdout(), "   └─ %s\n", ami)
	}
	if m.Provider == "gcp" && m.BaseImage == "" {
		m.BaseImage = gcpprovider.DefaultImageFamily
		fmt.Fprintf(cmd.OutOrStdout(), "🔍 Using default GCP image: Debian 12\n")
		fmt.Fprintf(cmd.OutOrStdout(), "   └─ %s\n", gcpprovider.DefaultImageFamily)
	}

	instanceType, err := resolveInstanceType(cmd.Context(), m)
	if err != nil {
		return err
	}

	cred, err := credential.Generate()
	if err != nil {
		return fmt.Errorf("generating keypair: %w", err)
	}

	ctx := cmd.Context()
	p, err := newProvider(ctx, provider.ProviderType(m.Provider), m.Region, "")
	if err != nil {
		return fmt.Errorf("initializing provider: %w", err)
	}
	// Before a relay slot or a machine exists: a machine with no public IP and
	// no NAT could never dial the relay, and would sit unreachable for its lease.
	if err := checkPrivateEgress(ctx, cmd.ErrOrStderr(), p, m); err != nil {
		return err
	}

	var relayToken string
	if m.Relay.Addr != "" {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return fmt.Errorf("generating relay token: %w", err)
		}
		relayToken = hex.EncodeToString(raw)

		// Pre-register the token with the relay before the instance boots.
		// This makes the CLI the authority: the relay rejects any connection
		// that presents a token the CLI has not explicitly registered.
		fmt.Fprintf(cmd.OutOrStdout(), "📡 Pre-registering relay slot...\n")
		if err := registerRelayToken(cmd.Context(), m.Relay.Addr, relayToken); err != nil {
			return fmt.Errorf("relay registration: %w", err)
		}
	}

	var ttlSeconds int
	if m.Reclaim.TTL != "" {
		if d, err := m.Reclaim.TTLDuration(); err == nil {
			ttlSeconds = int(d.Seconds())
		}
	}

	agentDigest, err := resolveAgentSHA256(m.Relay.AgentSHA256, agentSHA256)
	if err != nil {
		return err
	}
	if m.Relay.Addr != "" && agentDigest == "" {
		fmt.Fprintf(cmd.ErrOrStderr(), "⚠  The agent binary served by the relay will not be verified. Set relay.agentSha256 in the manifest, or use a release build of runlease.\n")
	}

	ud, err := userdata.Build(userdata.Params{
		AgentSHA256:       agentDigest,
		PublicKeySSH:      cred.PublicKeySSH,
		HostPrivateKeyPEM: cred.HostPrivateKeyPEM,
		HostPublicKeySSH:  cred.HostPublicKeySSH,
		RelayAddr:         m.Relay.Addr,
		RelayToken:        relayToken,
		Provider:          m.Provider,
		SelfTermination:   m.Reclaim.SelfTermination,
		TTLSeconds:        ttlSeconds,
		PasswordlessSudo:  m.Sudo.IsPasswordless(),
		SetupSteps:        toUserdataSteps(m.Setup),
	})
	if err != nil {
		return fmt.Errorf("building user-data: %w", err)
	}

	// Merge auto-tags (user tags take precedence on conflict).
	m.Tags = mergeAutoTags(m.Tags, m.Name, ttlSeconds)

	fmt.Fprintf(cmd.OutOrStdout(), "⚡ Requesting spot instance (%s in %s)...\n", instanceType, m.Region)
	handle, err := p.RequestSpot(ctx, provider.InstanceSpec{
		ManifestName:        m.Name,
		InstanceType:        instanceType,
		Region:              m.Region,
		ImageID:             m.BaseImage,
		SecurityGroupIDs:    m.AWS.SecurityGroupIDs,
		SubnetID:            subnetForProvider(m),
		NetworkID:           m.GCP.Network,
		UserData:            ud,
		Tags:                m.Tags,
		InstanceProfileARN:  m.AWS.InstanceProfile,
		ServiceAccountEmail: m.GCP.ServiceAccount,
		NetworkTags:         m.GCP.NetworkTags,
		PublicIP:            m.WantsPublicIP(),
		SelfTermination:     m.Reclaim.SelfTermination,
		DiskSizeGb:          m.DiskSizeGb,
	})
	if err != nil {
		return fmt.Errorf("requesting spot instance: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "   └─ %s — waiting for running state...\n", handle.ID)

	// Save credential before waiting so we can delete it if the wait fails.
	credDir := defaultCredDir()
	masterKey, err := credential.LoadOrCreateMasterKey(defaultMasterKeyPath())
	if err != nil {
		return fmt.Errorf("loading master key: %w", err)
	}
	if err := credential.Save(credDir, string(handle.ID), cred, masterKey); err != nil {
		return fmt.Errorf("saving credential: %w", err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, upTimeout)
	defer cancel()

	running, err := p.WaitUntilRunning(waitCtx, handle.ID)
	if err != nil {
		_ = credential.Delete(credDir, string(handle.ID))
		return fmt.Errorf("waiting for instance: %w\n(instance %s may still be running — check AWS console and terminate manually)", err, handle.ID)
	}

	// Restore fields that DescribeInstances doesn't return. CreatedAt comes from
	// the original RequestSpot handle — it anchors both the AGE column and the
	// projected LEASE cost (rate × full TTL) in `runlease list`.
	running.ManifestName = m.Name
	running.Tags = m.Tags
	running.InstanceType = instanceType
	running.CreatedAt = handle.CreatedAt
	// The project the instance was created in: later commands must use it, not
	// whatever the env / gcloud config says at that time.
	running.Project = handle.Project

	// Set expiry from TTL, anchored to when the instance was created.
	if m.Reclaim.TTL != "" {
		ttl, _ := m.Reclaim.TTLDuration()
		expiresAt := handle.CreatedAt.Add(ttl)
		running.ExpiresAt = &expiresAt
	}

	reg, err := loadRegistry()
	if err != nil {
		return err
	}
	if err := reg.Add(&registry.Entry{
		Handle:     *running,
		CredDir:    credDir,
		RelayAddr:  m.Relay.Addr,
		RelayToken: relayToken,
		Forwards:   manifestToRegistryForwards(m.Forwards),
	}); err != nil {
		return fmt.Errorf("updating registry: %w", err)
	}

	// Best-effort spot pricing — bounded so a slow/failed pricing API can never
	// hold up or fail the launch. Any error just means "no price shown".
	var leaseDur time.Duration
	if m.Reclaim.TTL != "" {
		leaseDur, _ = m.Reclaim.TTLDuration()
	}
	priceCtx, priceCancel := context.WithTimeout(ctx, 25*time.Second)
	rate, rateErr := pricing.SpotRate(priceCtx, runleaseHome(), provider.ProviderType(m.Provider), instanceType, m.Region)
	priceCancel()

	out := cmd.OutOrStdout()
	if jsonOutput() {
		if err := writeUpJSON(cmd, m, running, rate, rateErr, leaseDur); err != nil {
			return err
		}
		if detach {
			return nil
		}
		// Even in JSON mode, if the user wants to attach/connect we still do so.
	} else {
		fmt.Fprintf(out, "\n✅ Instance ready!\n")
		fmt.Fprintf(out, "   Name:      %s\n", m.Name)
		fmt.Fprintf(out, "   ID:        %s\n", running.ID)
		switch {
		case running.PublicIP != "" && m.Relay.Addr != "":
			fmt.Fprintf(out, "   Public IP: %s (outbound only: SSH listens on localhost)\n", running.PublicIP)
		case running.PublicIP != "":
			fmt.Fprintf(out, "   Public IP: %s\n", running.PublicIP)
		case m.Relay.Addr != "":
			fmt.Fprintf(out, "   Public IP: none\n")
		}
		if running.ExpiresAt != nil {
			fmt.Fprintf(out, "   Expires:   %s (%s)\n", running.ExpiresAt.Format(time.RFC3339), time.Until(*running.ExpiresAt).Round(time.Second))
		}
		if rateErr != nil {
			fmt.Fprintf(out, "   Rate:      unavailable — %s\n", pricingErrSummary(rateErr))
			if hint := pricing.RemediationHint(rateErr); hint != "" {
				fmt.Fprintf(out, "              ↳ for live rates, %s\n", hint)
			}
		} else {
			line := pricing.FormatHourly(rate)
			if leaseDur > 0 {
				line += fmt.Sprintf(" · %s for the %s lease", pricing.FormatLease(rate, leaseDur), fmtDuration(leaseDur))
			}
			fmt.Fprintf(out, "   Rate:      %s\n", line)
		}
		if m.Relay.Addr != "" {
			fmt.Fprintf(out, "\n🔌 Connect via relay:\n\n   runlease connect %s\n", m.Name)
		} else {
			fmt.Fprintf(out, "\n🔌 Connect:\n\n   runlease ssh %s\n", m.Name)
			if running.PublicIP == "" {
				fmt.Fprintf(out, "\n💡 No public IP assigned — add 'relay.addr' to your manifest for relay-based access,\n")
				fmt.Fprintf(out, "   or set 'publicIp: true' for direct SSH.\n")
			} else {
				fmt.Fprintf(out, "\n💡 Tip: add a relay to connect without a public IP next time:\n")
				fmt.Fprintf(out, "   runlease config set relay.addr wss://relay.runlease.online\n")
			}
		}

		fmt.Fprintf(out, "\n🛑 To terminate:\n")
		fmt.Fprintf(out, "   runlease down %s\n", m.Name)
		fmt.Fprintf(out, "   # or from inside the instance: sudo /usr/local/bin/runlease-terminate\n")
		if running.ExpiresAt != nil {
			fmt.Fprintf(out, "   # or wait — auto-terminates in %s\n", time.Until(*running.ExpiresAt).Round(time.Second))
		}
	}

	if detach {
		return nil
	}
	if attach {
		return runAttach(cmd, m, running, cred, relayToken)
	}
	// Manifest forwards auto-apply alongside the interactive shell. The
	// shell runs in the foreground; forwards run in a goroutine on the same
	// SSH client and are torn down when the shell exits.
	specs := registryForwardsToSpecs(manifestToRegistryForwards(m.Forwards))
	if m.Relay.Addr != "" {
		return pollAndConnectViaRelay(cmd, m.Relay.Addr, relayToken, m.Name, cred, specs)
	}
	return pollAndConnect(cmd, running.PublicIP, m.Name, cred, specs)
}

const sshPollInterval = 8 * time.Second

func pollAndConnect(cmd *cobra.Command, publicIP, name string, cred *credential.EphemeralCredential, forwards []forwardSpec) error {
	out := cmd.OutOrStdout()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	pollCtx, cancel := context.WithCancel(cmd.Context())
	defer cancel()

	go func() {
		select {
		case <-sigCh:
			cancel()
		case <-pollCtx.Done():
		}
	}()

	fmt.Fprintf(out, "\n⏳ Waiting for SSH to become ready (Ctrl+C to stop)...\n")
	for {
		d := &net.Dialer{Timeout: 5 * time.Second}
		conn, err := d.DialContext(pollCtx, "tcp", publicIP+":22")
		if err == nil {
			// The probe conn is the connection: sshShellWithForwards takes
			// ownership of it (and closes it on every path).
			sshErr := sshShellWithForwards(conn, publicIP+":22", cred, forwards, out)
			if sshErr != nil && strings.Contains(sshErr.Error(), "host key mismatch") {
				// sshd is up but the startup script hasn't injected our key yet.
				fmt.Fprintf(out, "🔄 sshd up, startup script still running — retrying in %s...\n", sshPollInterval)
			} else {
				return sshErr
			}
		}

		select {
		case <-pollCtx.Done():
			fmt.Fprintf(out, "\nStopped waiting. Connect later with: runlease ssh %s\n", name)
			return nil
		default:
		}

		fmt.Fprintf(out, "⏳ Not ready yet, retrying in %s...\n", sshPollInterval)
		select {
		case <-time.After(sshPollInterval):
		case <-pollCtx.Done():
			fmt.Fprintf(out, "\nStopped waiting. Connect later with: runlease ssh %s\n", name)
			return nil
		}
	}
}

func pollAndConnectViaRelay(cmd *cobra.Command, relayAddr, token, name string, cred *credential.EphemeralCredential, forwards []forwardSpec) error {
	out := cmd.OutOrStdout()
	readyURL := relayHTTPBase(relayAddr) + "/ready"

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	pollCtx, cancel := context.WithCancel(cmd.Context())
	defer cancel()

	go func() {
		select {
		case <-sigCh:
			cancel()
		case <-pollCtx.Done():
		}
	}()

	fmt.Fprintf(out, "\n⏳ Waiting for relay agent to register — usually takes ~30s (Ctrl+C to stop)...\n")
	for {
		// Poll via HTTP — doesn't consume the client slot.
		req, _ := http.NewRequestWithContext(pollCtx, "GET", readyURL, nil)
		req.Header.Set("X-Relay-Token", token)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			ready := resp.StatusCode == http.StatusOK
			resp.Body.Close()
			if ready {
				fmt.Fprintf(out, "🔗 Agent connected — opening SSH session...\n")
				conn, err := dialRelay(relayAddr, token)
				if err != nil {
					return fmt.Errorf("relay: %w", err)
				}
				return sshShellWithForwards(conn, relayAddr, cred, forwards, out)
			}
		}

		select {
		case <-pollCtx.Done():
			fmt.Fprintf(out, "\nStopped waiting. Connect later with: runlease connect %s\n", name)
			return nil
		default:
		}

		fmt.Fprintf(out, "⏳ Agent not ready yet, retrying in %s...\n", sshPollInterval)
		select {
		case <-time.After(sshPollInterval):
		case <-pollCtx.Done():
			fmt.Fprintf(out, "\nStopped waiting. Connect later with: runlease connect %s\n", name)
			return nil
		}
	}
}

// subnetForProvider resolves the single InstanceSpec.SubnetID field from the
// provider-specific manifest section. A valid manifest is single-provider:
// AWS reads aws.subnetID, GCP reads gcp.subnetwork, anything else has none.
func subnetForProvider(m *manifest.Manifest) string {
	switch provider.ProviderType(m.Provider) {
	case provider.ProviderGCP:
		return m.GCP.Subnetwork
	case provider.ProviderAWS:
		return m.AWS.SubnetID
	default:
		return ""
	}
}

func buildQuickManifest() (*manifest.Manifest, error) {
	cfg, _ := rlconfig.Load(defaultConfigPath())
	if cfg == nil {
		cfg = &rlconfig.Config{}
	}

	name := quickName
	if name == "" {
		b := make([]byte, 3)
		rand.Read(b) //nolint:errcheck
		name = fmt.Sprintf("rl-spot-%s", hex.EncodeToString(b))
	}

	size := quickSize
	if size == "compute.small" && cfg.Defaults.Size != "" {
		size = cfg.Defaults.Size
	}

	ttl := quickTTL
	if ttl == "2h" && cfg.Defaults.TTL != "" {
		ttl = cfg.Defaults.TTL
	}

	region := quickRegion
	if region == "" {
		region = cfg.Defaults.Region
	}
	if region == "" {
		switch quickProvider {
		case "gcp":
			region = "us-central1-a"
		case "aws":
			region = "us-east-1"
		default:
			return nil, fmt.Errorf("unknown provider %q — supported: aws, gcp", quickProvider)
		}
	}

	image := quickImage
	if image == "" {
		switch quickProvider {
		case "gcp":
			image = "projects/debian-cloud/global/images/family/debian-12"
		case "aws":
			// resolved later in runUp via DescribeImages
		default:
			return nil, fmt.Errorf("unknown provider %q — supported: aws, gcp", quickProvider)
		}
	}

	relayAddr := quickRelay
	if relayAddr == "" {
		relayAddr = cfg.Relay.Addr
	}

	if quickProvider != "gcp" && quickNetwork != "" {
		return nil, fmt.Errorf("--network is only supported for GCP")
	}

	// --subnet targets the provider's subnet: GCP subnetwork or AWS subnet ID.
	gcp := manifest.GCPConfig{ServiceAccount: quickServiceAccount, Network: quickNetwork}
	aws := manifest.AWSConfig{InstanceProfile: quickInstanceProfile}
	switch quickProvider {
	case "aws":
		aws.SubnetID = quickSubnet
	case "gcp":
		gcp.Subnetwork = quickSubnet
	}

	m := &manifest.Manifest{
		Name:          name,
		Provider:      quickProvider,
		Region:        region,
		InstanceClass: size,
		BaseImage:     image,
		DiskSizeGb:    quickDiskSize,
		Reclaim:       manifest.ReclaimPolicy{TTL: ttl, SelfTermination: quickSelfTerminate},
		GCP:           gcp,
		AWS:           aws,
	}
	if relayAddr != "" {
		m.Relay = manifest.RelayConfig{Addr: relayAddr}
	}
	// Manifest-file paths validate inside manifest.Load; quick mode builds the
	// struct directly, so validate here too (catches e.g. negative --disk-size
	// or a bad --ttl before we hit the cloud).
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return m, nil
}

// registerRelayToken pre-registers a token with the relay so the CLI is the
// issuing authority. The relay will reject any instance or client that presents
// a token that was not pre-registered here.
func registerRelayToken(ctx context.Context, relayAddr, token string) error {
	url := relayWSURL(relayAddr)

	// Present who we are: a saved 'runlease login' session, or failing that a
	// Google identity token from the environment or gcloud. The relay ignores
	// the header when auth is off, and rejects the request with a clear error
	// when auth is on and the token is missing or invalid.
	headers := http.Header{}
	bearer, bearerErr := relayBearer(ctx, relayAddr)
	if bearerErr == nil {
		headers.Set("Authorization", "Bearer "+bearer)
	}

	ws, _, err := websocket.DefaultDialer.Dial(url, headers)
	if err != nil {
		return fmt.Errorf("dial relay: %w", err)
	}
	defer ws.Close()

	msg, _ := json.Marshal(map[string]string{"role": "register", "token": token})
	ws.SetWriteDeadline(time.Now().Add(10 * time.Second)) //nolint:errcheck
	if err := ws.WriteMessage(websocket.BinaryMessage, append(msg, '\n')); err != nil {
		return fmt.Errorf("send: %w", err)
	}
	ws.SetReadDeadline(time.Now().Add(10 * time.Second)) //nolint:errcheck
	_, data, err := ws.ReadMessage()
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	var resp map[string]string
	if err := json.Unmarshal([]byte(strings.TrimRight(string(data), "\n\r")), &resp); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}
	switch resp["status"] {
	case "ok":
		return nil
	case "unauthorized":
		hint := "run 'runlease login' to sign in to this relay"
		if bearerErr != nil {
			hint += fmt.Sprintf(" (no identity was sent: %v)", bearerErr)
		}
		return fmt.Errorf("unauthorized: %s\n%s", resp["message"], hint)
	default:
		return fmt.Errorf("%s: %s", resp["status"], resp["message"])
	}
}

// googleIDToken returns a Google ID token for the given audience.
// Tries idtoken.NewTokenSource first (service accounts, workload identity),
// then falls back to `gcloud auth print-identity-token` (personal accounts).
func googleIDToken(ctx context.Context, audience string) (string, error) {
	if ts, err := idtoken.NewTokenSource(ctx, audience); err == nil {
		if tok, err := ts.Token(); err == nil {
			return tok.AccessToken, nil
		}
	}

	// Check whether gcloud is installed before calling it, so callers get a
	// clear "not installed" message rather than a confusing auth error.
	if _, err := exec.LookPath("gcloud"); err != nil {
		return "", fmt.Errorf("no Google credentials available: set GOOGLE_APPLICATION_CREDENTIALS or install gcloud and run 'gcloud auth application-default login'")
	}

	out, err := exec.CommandContext(ctx, "gcloud", "auth", "print-identity-token",
		"--audiences="+audience).Output()
	if err == nil {
		return strings.TrimSpace(string(out)), nil
	}
	// Personal accounts can't use --audiences; fall back to the default gcloud token.
	out, err = exec.CommandContext(ctx, "gcloud", "auth", "print-identity-token").Output()
	if err != nil {
		return "", fmt.Errorf("not authenticated with gcloud: run 'gcloud auth login'")
	}
	return strings.TrimSpace(string(out)), nil
}

// relayHTTPBase converts a relay addr to its HTTP base URL (for use as the
// ID token audience and agent download URL).
func relayHTTPBase(addr string) string {
	switch {
	case strings.HasPrefix(addr, "wss://"):
		return "https://" + strings.TrimPrefix(addr, "wss://")
	case strings.HasPrefix(addr, "ws://"):
		return "http://" + strings.TrimPrefix(addr, "ws://")
	default:
		return "http://" + addr
	}
}

// relayWSURL converts a relay addr (host:port or ws[s]://...) to a WebSocket URL.
func relayWSURL(addr string) string {
	if strings.HasPrefix(addr, "ws://") || strings.HasPrefix(addr, "wss://") {
		if !strings.Contains(addr[6:], "/") {
			return addr + "/relay"
		}
		return addr
	}
	return "ws://" + addr + "/relay"
}

// dialRelay connects to the relay as a client and returns a net.Conn ready for
// SSH once the relay responds "connected".
func dialRelay(addr, token string) (net.Conn, error) {
	url := relayWSURL(addr)
	ws, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}

	msg, _ := json.Marshal(map[string]string{"role": "client", "token": token})
	ws.SetWriteDeadline(time.Now().Add(30 * time.Second)) //nolint:errcheck
	if err := ws.WriteMessage(websocket.BinaryMessage, append(msg, '\n')); err != nil {
		ws.Close()
		return nil, fmt.Errorf("send handshake: %w", err)
	}
	ws.SetReadDeadline(time.Now().Add(30 * time.Second)) //nolint:errcheck
	_, data, err := ws.ReadMessage()
	ws.SetReadDeadline(time.Time{}) //nolint:errcheck
	if err != nil {
		ws.Close()
		return nil, fmt.Errorf("read response: %w", err)
	}
	var resp map[string]string
	if err := json.Unmarshal([]byte(strings.TrimRight(string(data), "\n\r")), &resp); err != nil {
		ws.Close()
		return nil, fmt.Errorf("parse response: %w", err)
	}
	if resp["status"] != "connected" {
		ws.Close()
		return nil, fmt.Errorf("%s: %s", resp["status"], resp["message"])
	}
	return relay.NewWSConn(ws), nil
}

func resolveInstanceType(ctx context.Context, m *manifest.Manifest) (string, error) {
	if m.InstanceType != "" {
		return m.InstanceType, nil
	}
	resolver, err := instanceclass.LoadDefault()
	if err != nil {
		return "", fmt.Errorf("loading instance class catalog: %w", err)
	}
	candidates, err := resolver.Resolve(m.InstanceClass, provider.ProviderType(m.Provider))
	if err != nil {
		return "", fmt.Errorf("resolving instance class %q: %w", m.InstanceClass, err)
	}
	_ = ctx // reserved for price-aware selection in M2
	return candidates[0], nil
}

// mergeAutoTags returns a new tag map with runlease system tags pre-populated.
// Any key already set by the user is left unchanged.
func mergeAutoTags(userTags map[string]string, name string, ttlSeconds int) map[string]string {
	tags := map[string]string{
		"runlease-managed":    "true",
		"runlease-created-by": currentUsername(),
	}
	if ttlSeconds > 0 {
		tags["runlease-expires-at"] = time.Now().UTC().Add(time.Duration(ttlSeconds) * time.Second).Format(time.RFC3339)
	}
	for k, v := range userTags {
		tags[k] = v
	}
	return tags
}

// manifestToRegistryForwards copies manifest.Forwards into the registry's
// independent ForwardSpec shape (avoids a registry → manifest import cycle).
func manifestToRegistryForwards(in []manifest.ForwardSpec) []registry.ForwardSpec {
	if len(in) == 0 {
		return nil
	}
	out := make([]registry.ForwardSpec, len(in))
	for i, f := range in {
		out[i] = registry.ForwardSpec{
			Name: f.Name, Direction: f.Direction, Bind: f.Bind,
			Local: f.Local, Remote: f.Remote, Host: f.Host,
		}
	}
	return out
}

// writeUpJSON emits a compact, stable JSON record describing the new instance.
// Used by CI/scripts via `runlease up --output json`. Pricing fields are
// omitted when pricing was unavailable.
func writeUpJSON(cmd *cobra.Command, m *manifest.Manifest, running *provider.InstanceHandle, rate pricing.Rate, rateErr error, leaseDur time.Duration) error {
	type rec struct {
		Name         string   `json:"name"`
		ID           string   `json:"id"`
		Provider     string   `json:"provider"`
		InstanceType string   `json:"instanceType,omitempty"`
		Region       string   `json:"region"`
		PublicIP     string   `json:"publicIp,omitempty"`
		PrivateIP    string   `json:"privateIp,omitempty"`
		Relay        string   `json:"relay,omitempty"`
		ExpiresAt    string   `json:"expiresAt,omitempty"`
		USDPerHour   *float64 `json:"usdPerHour,omitempty"`
		LeaseCostUSD *float64 `json:"leaseCostUsd,omitempty"`
		PriceApprox  bool     `json:"priceApprox,omitempty"`
	}
	r := rec{
		Name:         m.Name,
		ID:           string(running.ID),
		Provider:     m.Provider,
		InstanceType: running.InstanceType,
		Region:       m.Region,
		PublicIP:     running.PublicIP,
		PrivateIP:    running.PrivateIP,
		Relay:        m.Relay.Addr,
	}
	if running.ExpiresAt != nil {
		r.ExpiresAt = running.ExpiresAt.UTC().Format(time.RFC3339)
	}
	if rateErr == nil {
		hourly := rate.USDPerHour
		r.USDPerHour = &hourly
		r.PriceApprox = rate.Approx
		if leaseDur > 0 {
			lease := rate.LeaseCost(leaseDur)
			r.LeaseCostUSD = &lease
		}
	}
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// pricingErrSummary condenses a pricing error to a single short line for the
// status output (full errors can be multi-clause from wrapped SDK messages).
func pricingErrSummary(err error) string {
	msg := err.Error()
	if i := strings.IndexAny(msg, "\n"); i >= 0 {
		msg = msg[:i]
	}
	const max = 80
	if len(msg) > max {
		msg = msg[:max] + "…"
	}
	return msg
}

// toUserdataSteps converts manifest setup steps into the simpler shape the
// userdata template expects. File: references have already been inlined at
// manifest load time.
func toUserdataSteps(steps []manifest.SetupStep) []userdata.SetupStep {
	if len(steps) == 0 {
		return nil
	}
	out := make([]userdata.SetupStep, len(steps))
	for i, s := range steps {
		out[i] = userdata.SetupStep{Name: s.Name, Run: s.Run}
	}
	return out
}

func currentUsername() string {
	if u, err := user.Current(); err == nil {
		name := u.Username
		// Windows may return "DOMAIN\user"
		if idx := strings.LastIndex(name, "\\"); idx >= 0 {
			name = name[idx+1:]
		}
		return name
	}
	return "unknown"
}

// agentSHA256 is the SHA-256 of the linux/amd64 runlease-agent built from the
// same release as this CLI. Release builds set it with
// -ldflags "-X main.agentSHA256=<hex>"; it is empty in a plain "go build".
var agentSHA256 string

// agentUnverified is the manifest value that turns the agent check off on
// purpose, for a relay that serves a self-built agent.
const agentUnverified = "unverified"

// resolveAgentSHA256 picks the digest the boot script checks the relay-served
// agent against. A manifest value wins over the build-time one. It returns ""
// when the check is skipped: either the manifest says "unverified", or neither
// source has a digest (a development build).
func resolveAgentSHA256(fromManifest, fromBuild string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(fromManifest))
	if v == agentUnverified {
		return "", nil
	}
	if v == "" {
		v = strings.ToLower(strings.TrimSpace(fromBuild))
	}
	if v == "" {
		return "", nil
	}
	if len(v) != 64 || strings.Trim(v, "0123456789abcdef") != "" {
		return "", fmt.Errorf("relay.agentSha256 must be 64 hex characters or %q", agentUnverified)
	}
	return v, nil
}

// checkPrivateEgress stops a relay-mode launch that takes the default of no
// public IP on a network with no NAT: the agent could not reach the relay, so
// the machine would be unreachable and still billed until its lease ran out.
//
// It only blocks when it is sure. A check that cannot be made (no permission to
// list routers, a provider that has no check) prints a note and lets the launch
// go on, and a manifest that sets publicIp itself is taken at its word.
func checkPrivateEgress(ctx context.Context, errOut io.Writer, p provider.CloudProvider, m *manifest.Manifest) error {
	if m.Relay.Addr == "" || m.WantsPublicIP() {
		return nil
	}
	const note = "⚠  This machine gets no public IP. It reaches the relay through your subnet's NAT gateway; without one it will never connect.\n   To give it a public IP for outbound traffic, set 'publicIp: true' (SSH still listens on localhost only).\n"
	checker, ok := p.(provider.EgressChecker)
	if !ok || m.PublicIPExplicit() {
		fmt.Fprint(errOut, note)
		return nil
	}
	checkCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	has, err := checker.HasPrivateEgress(checkCtx, m.GCP.Network, m.GCP.Subnetwork)
	if err != nil {
		fmt.Fprint(errOut, note)
		fmt.Fprintf(errOut, "   (could not check for a NAT gateway: %v)\n", err)
		return nil
	}
	if !has {
		return fmt.Errorf("no Cloud NAT found for this network in %s, and by default a relay-mode machine gets no public IP, so it could not reach the relay.\n"+
			"Do one of:\n"+
			"  - add Cloud NAT to the network (the machine then has no public address at all)\n"+
			"  - set 'publicIp: true' in the manifest, or pass --public-ip (outbound only: SSH listens on localhost)\n"+
			"  - set 'publicIp: false' if the network gets out some other way", m.Region)
	}
	return nil
}
