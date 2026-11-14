package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validYAML = `
name: my-runner
provider: aws
region: us-east-1
instanceType: c5.xlarge
baseImage: ami-0abcdef1234567890
reclaim:
  ttl: 2h
tags:
  env: ci
`

func TestLoad_valid(t *testing.T) {
	f := writeTempFile(t, validYAML)
	m, err := Load(f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Name != "my-runner" {
		t.Errorf("name = %q, want %q", m.Name, "my-runner")
	}
	if m.Provider != "aws" {
		t.Errorf("provider = %q, want %q", m.Provider, "aws")
	}
	if m.InstanceType != "c5.xlarge" {
		t.Errorf("instanceType = %q, want %q", m.InstanceType, "c5.xlarge")
	}
	if m.Tags["env"] != "ci" {
		t.Errorf("tags[env] = %q, want %q", m.Tags["env"], "ci")
	}
}

func TestLoad_fileNotFound(t *testing.T) {
	_, err := Load("/no/such/file.yaml")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestLoad_invalidYAML(t *testing.T) {
	f := writeTempFile(t, "not: valid: yaml: [[[")
	_, err := Load(f)
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
}

func TestValidate_missingFields(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Manifest)
		wantErr string
	}{
		{"no name", func(m *Manifest) { m.Name = "" }, "manifest.name is required"},
		{"no provider", func(m *Manifest) { m.Provider = "" }, "manifest.provider is required"},
		{"no region", func(m *Manifest) { m.Region = "" }, "manifest.region is required"},
		{"no instance selector", func(m *Manifest) { m.InstanceType = "" }, "one of manifest.instanceType or manifest.instanceClass is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := writeTempFile(t, validYAML)
			m, err := Load(f)
			if err != nil {
				t.Fatalf("setup: %v", err)
			}
			tc.mutate(m)
			if err := m.Validate(); err == nil || err.Error() != tc.wantErr {
				t.Errorf("Validate() = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

const classYAML = `
name: my-runner
provider: aws
region: us-east-1
instanceClass: compute.large
baseImage: ami-0abcdef1234567890
`

func TestLoad_withInstanceClass(t *testing.T) {
	f := writeTempFile(t, classYAML)
	m, err := Load(f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.InstanceClass != "compute.large" {
		t.Errorf("instanceClass = %q, want %q", m.InstanceClass, "compute.large")
	}
	if m.InstanceType != "" {
		t.Errorf("instanceType should be empty when instanceClass is set")
	}
}

func TestValidate_bothSelectorsMutuallyExclusive(t *testing.T) {
	f := writeTempFile(t, validYAML)
	m, err := Load(f)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	m.InstanceClass = "compute.large"
	err = m.Validate()
	if err == nil {
		t.Fatal("expected error when both instanceType and instanceClass are set")
	}
}

func TestReclaimPolicy_TTLDuration(t *testing.T) {
	r := ReclaimPolicy{TTL: "90m"}
	d, err := r.TTLDuration()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.Minutes() != 90 {
		t.Errorf("TTLDuration = %v, want 90m", d)
	}

	r2 := ReclaimPolicy{TTL: ""}
	d2, err := r2.TTLDuration()
	if err != nil || d2 != 0 {
		t.Errorf("empty TTL: got (%v, %v), want (0, nil)", d2, err)
	}
}

const gcpYAML = `
name: gcp-runner
provider: gcp
region: us-central1-a
instanceType: n2-standard-4
baseImage: projects/ubuntu-os-cloud/global/images/family/ubuntu-2404-lts-amd64
gcp:
  serviceAccount: deployer@project.iam.gserviceaccount.com
  networkTags:
    - allow-ssh
    - runlease
  network: my-vpc
  subnetwork: my-subnet
relay:
  addr: relay.example.com:7777
`

func TestLoad_gcpFields(t *testing.T) {
	f := writeTempFile(t, gcpYAML)
	m, err := Load(f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Provider != "gcp" {
		t.Errorf("provider = %q, want gcp", m.Provider)
	}
	if m.GCP.ServiceAccount != "deployer@project.iam.gserviceaccount.com" {
		t.Errorf("gcp.serviceAccount = %q", m.GCP.ServiceAccount)
	}
	if len(m.GCP.NetworkTags) != 2 || m.GCP.NetworkTags[0] != "allow-ssh" {
		t.Errorf("gcp.networkTags = %v", m.GCP.NetworkTags)
	}
	if m.GCP.Network != "my-vpc" {
		t.Errorf("gcp.network = %q, want my-vpc", m.GCP.Network)
	}
	if m.GCP.Subnetwork != "my-subnet" {
		t.Errorf("gcp.subnetwork = %q, want my-subnet", m.GCP.Subnetwork)
	}
	if m.Relay.Addr != "relay.example.com:7777" {
		t.Errorf("relay.addr = %q", m.Relay.Addr)
	}
}

func TestSetup_inlineRun(t *testing.T) {
	src := validYAML + `
setup:
  - name: install
    run: |
      echo hello
`
	f := writeTempFile(t, src)
	m, err := Load(f)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(m.Setup) != 1 {
		t.Fatalf("len(setup) = %d, want 1", len(m.Setup))
	}
	if m.Setup[0].Name != "install" {
		t.Errorf("name = %q, want install", m.Setup[0].Name)
	}
	if !strings.Contains(m.Setup[0].Run, "echo hello") {
		t.Errorf("run = %q, want to contain 'echo hello'", m.Setup[0].Run)
	}
}

func TestSetup_fileResolvedRelative(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "seed.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\necho seeded\n"), 0644); err != nil {
		t.Fatalf("write script: %v", err)
	}
	manifestPath := filepath.Join(dir, "manifest.yaml")
	if err := os.WriteFile(manifestPath, []byte(validYAML+"\nsetup:\n  - file: seed.sh\n"), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	m, err := Load(manifestPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !strings.Contains(m.Setup[0].Run, "echo seeded") {
		t.Errorf("file: was not inlined into run: got %q", m.Setup[0].Run)
	}
	if m.Setup[0].File != "" {
		t.Errorf("File should be cleared after inlining, got %q", m.Setup[0].File)
	}
}

func TestSetup_invalidMutualExclusion(t *testing.T) {
	src := validYAML + `
setup:
  - run: "echo a"
    file: nope.sh
`
	f := writeTempFile(t, src)
	_, err := Load(f)
	if err == nil {
		t.Fatal("expected error when both run and file are set")
	}
}

func TestSudoMode_defaultsAndValid(t *testing.T) {
	cases := []struct {
		mode   SudoMode
		ok     bool
		pwLess bool
	}{
		{"", true, false},
		{SudoLocked, true, false},
		{SudoPasswordless, true, true},
		{"yolo", false, false},
	}
	for _, tc := range cases {
		t.Run(string(tc.mode), func(t *testing.T) {
			m := &Manifest{
				Name: "n", Provider: "aws", Region: "us-east-1",
				InstanceType: "t3.micro", BaseImage: "ami-x",
				Sudo: tc.mode,
			}
			err := m.Validate()
			if tc.ok && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("Validate() = nil, want error")
			}
			if got := m.Sudo.IsPasswordless(); got != tc.pwLess {
				t.Errorf("IsPasswordless() = %v, want %v", got, tc.pwLess)
			}
		})
	}
}

func TestValidate_invalidTTL(t *testing.T) {
	f := writeTempFile(t, validYAML)
	m, err := Load(f)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	m.Reclaim.TTL = "not-a-duration"
	if err := m.Validate(); err == nil {
		t.Fatal("expected error for invalid TTL")
	}
}

func TestValidate_diskSize(t *testing.T) {
	f := writeTempFile(t, validYAML)
	m, _ := Load(f)
	m.DiskSizeGb = -1
	if err := m.Validate(); err == nil {
		t.Fatal("expected error for negative diskSizeGb")
	}
	m.DiskSizeGb = 0 // image default
	if err := m.Validate(); err != nil {
		t.Errorf("diskSizeGb=0 should be valid: %v", err)
	}
	m.DiskSizeGb = 100
	if err := m.Validate(); err != nil {
		t.Errorf("diskSizeGb=100 should be valid: %v", err)
	}
}

func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "manifest-*.yaml")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("writing temp file: %v", err)
	}
	f.Close()
	return f.Name()
}
