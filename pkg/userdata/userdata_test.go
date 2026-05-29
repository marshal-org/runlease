package userdata

import (
	"strconv"
	"strings"
	"testing"
)

const testPubKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFake/Key/For/Testing runlease-test"
const testHostPubKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFake/Host/Key/For/Testing runlease-host"

var testHostPrivPEM = []byte("-----BEGIN PRIVATE KEY-----\nfake\n-----END PRIVATE KEY-----\n")

func fullParams() Params {
	return Params{
		PublicKeySSH:      testPubKey,
		HostPrivateKeyPEM: testHostPrivPEM,
		HostPublicKeySSH:  testHostPubKey,
	}
}

func TestBuild_containsAgentPublicKey(t *testing.T) {
	out, err := Build(fullParams())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !strings.Contains(out, testPubKey) {
		t.Errorf("output does not contain agent public key")
	}
}

func TestBuild_containsHostPublicKey(t *testing.T) {
	out, err := Build(fullParams())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !strings.Contains(out, testHostPubKey) {
		t.Errorf("output does not contain host public key")
	}
}

func TestBuild_containsBase64HostPrivateKey(t *testing.T) {
	out, err := Build(fullParams())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	// Private key must appear base64-encoded, not as raw PEM.
	if strings.Contains(out, "-----BEGIN PRIVATE KEY-----") {
		t.Error("raw PEM block should not appear in output — must be base64 encoded")
	}
	if !strings.Contains(out, "base64 -d") {
		t.Error("output should decode the base64-encoded host key")
	}
}

func TestBuild_removesOtherHostKeyTypes(t *testing.T) {
	out, err := Build(fullParams())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !strings.Contains(out, "ssh_host_rsa_key") || !strings.Contains(out, "rm -f") {
		t.Error("script should remove RSA host keys")
	}
}

func TestBuild_containsHardeningDirectives(t *testing.T) {
	out, err := Build(fullParams())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, want := range []string{
		"PasswordAuthentication no",
		"PubkeyAuthentication yes",
		"AllowUsers runlease-agent",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q", want)
		}
	}
}

func TestBuild_startsWithShebang(t *testing.T) {
	out, err := Build(fullParams())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !strings.HasPrefix(out, "#!/bin/bash") {
		t.Errorf("output does not start with shebang")
	}
}

func TestBuild_missingPublicKey(t *testing.T) {
	p := fullParams()
	p.PublicKeySSH = ""
	if _, err := Build(p); err == nil {
		t.Fatal("expected error for empty PublicKeySSH")
	}
}

func TestBuild_missingHostPrivateKey(t *testing.T) {
	p := fullParams()
	p.HostPrivateKeyPEM = nil
	if _, err := Build(p); err == nil {
		t.Fatal("expected error for missing HostPrivateKeyPEM")
	}
}

func TestBuild_relaySection(t *testing.T) {
	p := fullParams()
	p.RelayAddr = "relay.example.com:7777"
	p.RelayToken = "abc123"

	out, err := Build(p)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	for _, want := range []string{
		"RUNLEASE_RELAY_ADDR=relay.example.com:7777",
		"RUNLEASE_RELAY_TOKEN=abc123",
		"http://relay.example.com:7777/agent",
		"runlease-agent.service",
		"systemctl enable --now runlease-agent",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("relay output missing %q", want)
		}
	}
}

func TestBuild_noRelaySection(t *testing.T) {
	out, err := Build(fullParams())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, absent := range []string{
		"RUNLEASE_RELAY_ADDR",
		"runlease-agent.service",
	} {
		if strings.Contains(out, absent) {
			t.Errorf("non-relay output should not contain %q", absent)
		}
	}
}

func TestBuild_gcpTTLBlock(t *testing.T) {
	p := fullParams()
	p.Provider = "gcp"
	p.TTLSeconds = 7200
	p.SelfTermination = true

	out, err := Build(p)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, want := range []string{
		"systemd-run --unit=runlease-selfterminate --on-active=7200",
		"/usr/local/bin/runlease-terminate",
		"metadata.google.internal",
		"compute.googleapis.com",
		"runlease-terminate",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("GCP TTL output missing %q", want)
		}
	}
	if strings.Contains(out, "shutdown -h now") {
		t.Error("GCP TTL output should not contain AWS shutdown command")
	}
}

func TestBuild_awsTTLBlock(t *testing.T) {
	p := fullParams()
	p.Provider = "aws"
	p.TTLSeconds = 3600
	p.SelfTermination = true

	out, err := Build(p)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, want := range []string{
		"systemd-run --unit=runlease-selfterminate --on-active=3600",
		"/usr/local/bin/runlease-terminate",
		"systemctl --force --force poweroff",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("AWS TTL output missing %q", want)
		}
	}
	if strings.Contains(out, "metadata.google.internal") {
		t.Error("AWS TTL output should not contain GCP metadata URL")
	}
}

func TestBuild_setupStepsRendered(t *testing.T) {
	p := fullParams()
	p.SetupSteps = []SetupStep{
		{Name: "install-k3s", Run: "curl -sfL https://get.k3s.io | sh -"},
		{Name: "", Run: "echo unnamed-step"},
	}
	out, err := Build(p)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, want := range []string{
		"curl -sfL https://get.k3s.io",
		"echo unnamed-step",
		"/var/lib/runlease/setup",
		"/var/log/runlease-setup.log",
		"touch /run/runlease-setup-done",
		"00-install-k3s.sh",
		"01.sh",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("setup output missing %q", want)
		}
	}
}

func TestBuild_noSetupStepsIsClean(t *testing.T) {
	out, err := Build(fullParams())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, absent := range []string{
		"/var/lib/runlease/setup",
		"runlease-setup-done",
	} {
		if strings.Contains(out, absent) {
			t.Errorf("output without setup steps should not contain %q", absent)
		}
	}
}

func TestBuild_passwordlessSudo(t *testing.T) {
	p := fullParams()
	p.PasswordlessSudo = true
	out, err := Build(p)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !strings.Contains(out, "runlease-agent ALL=(ALL) NOPASSWD: ALL") {
		t.Error("output missing NOPASSWD: ALL directive")
	}
	if !strings.Contains(out, "/etc/sudoers.d/runlease-agent-full") {
		t.Error("output missing sudoers.d drop-in path")
	}
}

func TestBuild_lockedSudoIsDefault(t *testing.T) {
	out, err := Build(fullParams())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if strings.Contains(out, "NOPASSWD: ALL") {
		t.Error("default output should not grant NOPASSWD: ALL")
	}
	if strings.Contains(out, "/etc/sudoers.d/runlease-agent-full") {
		t.Error("default output should not write the full sudoers drop-in")
	}
}

func TestBuild_noTTLBlock(t *testing.T) {
	p := fullParams()
	p.Provider = "gcp"
	p.TTLSeconds = 0
	p.SelfTermination = true

	out, err := Build(p)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	// terminate script is always installed
	if !strings.Contains(out, "runlease-terminate") {
		t.Error("terminate script should always be installed")
	}
	// but the auto-fire boot timer should not be scheduled. The snooze helper
	// always carries a `systemd-run --unit=runlease-selfterminate
	// --on-active="$SECS"` line, so key the check on the numeric offset, which
	// only the boot timer emits.
	if strings.Contains(out, "--on-active=0 /usr/local/bin/runlease-terminate") {
		t.Error("zero-TTL output should not schedule the auto-fire boot timer")
	}
}

// TestBuild_terminateScriptInstalledWithoutSelfTerm verifies the new contract:
// the runlease-terminate manual-escape script is installed on every VM
// regardless of `reclaim.selfTermination`. Only the auto-fire timer is gated.
func TestBuild_terminateScriptInstalledWithoutSelfTerm(t *testing.T) {
	p := fullParams()
	p.Provider = "gcp"
	p.SelfTermination = false
	p.TTLSeconds = 0

	out, err := Build(p)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, want := range []string{
		"/usr/local/bin/runlease-terminate",
		"/etc/sudoers.d/runlease-terminate",
		"NOPASSWD: /usr/local/bin/runlease-terminate",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("script/sudoers should be installed regardless of SelfTermination; missing %q", want)
		}
	}
	if strings.Contains(out, "--on-active=0 /usr/local/bin/runlease-terminate") {
		t.Error("auto-fire timer must NOT be scheduled when SelfTermination is false")
	}
}

func TestBuild_autoFireTimerGated(t *testing.T) {
	// SelfTermination=true alone is not enough; TTLSeconds must also be > 0.
	cases := []struct {
		name       string
		selfTerm   bool
		ttlSeconds int
		wantTimer  bool
	}{
		{"both off", false, 0, false},
		{"selfTerm on, ttl zero", true, 0, false},
		{"selfTerm off, ttl set (no auto-fire)", false, 3600, false},
		{"both on", true, 3600, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := fullParams()
			p.Provider = "gcp"
			p.SelfTermination = tc.selfTerm
			p.TTLSeconds = tc.ttlSeconds
			out, err := Build(p)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			// Key on the numeric offset + terminate path: only the gated boot
			// timer emits it. The always-present snooze helper uses "$SECS".
			marker := "--on-active=" + strconv.Itoa(tc.ttlSeconds) + " /usr/local/bin/runlease-terminate"
			has := strings.Contains(out, marker)
			if has != tc.wantTimer {
				t.Errorf("auto-fire timer presence = %v, want %v", has, tc.wantTimer)
			}
		})
	}
}
