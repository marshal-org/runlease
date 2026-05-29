package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

	rlconfig "github.com/runlease/runlease/pkg/config"
	"github.com/runlease/runlease/pkg/credential"
	"github.com/runlease/runlease/pkg/instanceclass"
	"github.com/runlease/runlease/pkg/manifest"
	"github.com/runlease/runlease/pkg/provider"
	awsprovider "github.com/runlease/runlease/pkg/provider/aws"
	gcpprovider "github.com/runlease/runlease/pkg/provider/gcp"
	"github.com/runlease/runlease/pkg/registry"
	"github.com/runlease/runlease/pkg/relay"
	"github.com/runlease/runlease/pkg/userdata"
)

var (
	manifestPath  string
	upTimeout     time.Duration
	detach        bool
	attach        bool
	quickProvider string
	quickRegion   string
	quickSize     string
	quickImage          string
	quickRelay          string
	quickTTL            string
	quickName           string
	quickSelfTerminate  bool
	quickServiceAccount string
	quickInstanceProfile string
)

func init() {
	upCmd.Flags().StringVarP(&manifestPath, "manifest", "m", "", "path to manifest YAML")
	upCmd.Flags().DurationVar(&upTimeout, "timeout", 5*time.Minute, "timeout waiting for instance to become ready")
	upCmd.Flags().BoolVarP(&detach, "detach", "d", false, "exit after provisioning without waiting for SSH")
	upCmd.Flags().BoolVar(&attach, "attach", false, "after provisioning, tail setup logs and apply manifest.forwards (blocks until Ctrl+C)")
	upCmd.Flags().StringVarP(&quickProvider, "provider", "p", "", "provider: aws or gcp (quick mode — no manifest needed)")
	upCmd.Flags().StringVarP(&quickRegion, "region", "r", "", "region/zone (quick mode; defaults: us-east-1 for aws, us-central1-a for gcp)")
	upCmd.Flags().StringVarP(&quickSize, "size", "s", "compute.small", "instance class (quick mode)")
	upCmd.Flags().StringVar(&quickImage, "image", "", "base image ID (quick mode; GCP defaults to debian-12)")
	upCmd.Flags().StringVar(&quickRelay, "relay", "", "relay address, e.g. wss://relay.example.com (quick mode)")
	upCmd.Flags().StringVar(&quickTTL, "ttl", "2h", "instance TTL (quick mode)")
	upCmd.Flags().StringVarP(&quickName, "name", "n", "", "instance name (quick mode; auto-generated if empty)")
	upCmd.Flags().BoolVar(&quickSelfTerminate, "self-terminate", true, "allow the instance to delete itself (required for TTL to work)")
	upCmd.Flags().StringVar(&quickServiceAccount, "service-account", "", "GCP service account email to attach (quick mode)")
	upCmd.Flags().StringVar(&quickInstanceProfile, "instance-profile", "", "AWS IAM instance profile ARN to attach (quick mode)")
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

	ud, err := userdata.Build(userdata.Params{
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

	ctx := cmd.Context()
	p, err := newProvider(ctx, provider.ProviderType(m.Provider), m.Region)
	if err != nil {
		return fmt.Errorf("initializing provider: %w", err)
	}

	// Merge auto-tags (user tags take precedence on conflict).
	m.Tags = mergeAutoTags(m.Tags, m.Name, ttlSeconds)

	if !m.WantsPublicIP() && provider.ProviderType(m.Provider) == provider.ProviderGCP {
		fmt.Fprintf(cmd.OutOrStdout(), "⚠️  Launching without a public IP — ensure Cloud NAT is configured so the instance can reach the relay.\n")
	}
	fmt.Fprintf(cmd.OutOrStdout(), "⚡ Requesting spot instance (%s in %s)...\n", instanceType, m.Region)
	handle, err := p.RequestSpot(ctx, provider.InstanceSpec{
		ManifestName:        m.Name,
		InstanceType:        instanceType,
		Region:              m.Region,
		ImageID:             m.BaseImage,
		SecurityGroupIDs:    m.AWS.SecurityGroupIDs,
		SubnetID:            m.AWS.SubnetID,
		UserData:            ud,
		Tags:                m.Tags,
		InstanceProfileARN:  m.AWS.InstanceProfile,
		ServiceAccountEmail: m.GCP.ServiceAccount,
		NetworkTags:         m.GCP.NetworkTags,
		PublicIP:            m.WantsPublicIP(),
		SelfTermination:     m.Reclaim.SelfTermination,
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

	// Restore fields that DescribeInstances doesn't return.
	running.ManifestName = m.Name
	running.Tags = m.Tags

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

	out := cmd.OutOrStdout()
	if jsonOutput() {
		if err := writeUpJSON(cmd, m, running); err != nil {
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
		if running.PublicIP != "" {
			fmt.Fprintf(out, "   Public IP: %s\n", running.PublicIP)
		}
		if running.ExpiresAt != nil {
			fmt.Fprintf(out, "   Expires:   %s (%s)\n", running.ExpiresAt.Format(time.RFC3339), time.Until(*running.ExpiresAt).Round(time.Second))
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

	m := &manifest.Manifest{
		Name:          name,
		Provider:      quickProvider,
		Region:        region,
		InstanceClass: size,
		BaseImage:     image,
		Reclaim:       manifest.ReclaimPolicy{TTL: ttl, SelfTermination: quickSelfTerminate},
		GCP:           manifest.GCPConfig{ServiceAccount: quickServiceAccount},
		AWS:           manifest.AWSConfig{InstanceProfile: quickInstanceProfile},
	}
	if relayAddr != "" {
		m.Relay = manifest.RelayConfig{Addr: relayAddr}
	}
	return m, nil
}

// registerRelayToken pre-registers a token with the relay so the CLI is the
// issuing authority. The relay will reject any instance or client that presents
// a token that was not pre-registered here.
func registerRelayToken(ctx context.Context, relayAddr, token string) error {
	url := relayWSURL(relayAddr)

	// Attach a Google ID token if ADC credentials are available. The relay
	// ignores the header when auth is disabled, and rejects the request with
	// a clear error when auth is enabled and the token is missing or invalid.
	headers := http.Header{}
	if idTok, err := googleIDToken(ctx, relayHTTPBase(relayAddr)); err == nil {
		headers.Set("Authorization", "Bearer "+idTok)
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
	if resp["status"] != "ok" {
		return fmt.Errorf("%s: %s", resp["status"], resp["message"])
	}
	return nil
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
// Used by CI/scripts via `runlease up --output json`.
func writeUpJSON(cmd *cobra.Command, m *manifest.Manifest, running *provider.InstanceHandle) error {
	type rec struct {
		Name      string `json:"name"`
		ID        string `json:"id"`
		Provider  string `json:"provider"`
		Region    string `json:"region"`
		PublicIP  string `json:"publicIp,omitempty"`
		PrivateIP string `json:"privateIp,omitempty"`
		Relay     string `json:"relay,omitempty"`
		ExpiresAt string `json:"expiresAt,omitempty"`
	}
	r := rec{
		Name:      m.Name,
		ID:        string(running.ID),
		Provider:  m.Provider,
		Region:    m.Region,
		PublicIP:  running.PublicIP,
		PrivateIP: running.PrivateIP,
		Relay:     m.Relay.Addr,
	}
	if running.ExpiresAt != nil {
		r.ExpiresAt = running.ExpiresAt.UTC().Format(time.RFC3339)
	}
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(r)
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
