package userdata

import (
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

	out, err := Build(p)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, want := range []string{
		"systemd-run --on-active=7200",
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

	out, err := Build(p)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, want := range []string{
		"systemd-run --on-active=3600",
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

func TestBuild_noTTLBlock(t *testing.T) {
	p := fullParams()
	p.Provider = "gcp"
	p.TTLSeconds = 0

	out, err := Build(p)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	// terminate script is always installed even without TTL
	if !strings.Contains(out, "runlease-terminate") {
		t.Error("terminate script should always be installed")
	}
	// but the systemd-run timer should not be scheduled
	if strings.Contains(out, "systemd-run --on-active") {
		t.Error("zero-TTL output should not schedule a systemd-run timer")
	}
}
