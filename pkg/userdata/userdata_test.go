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
