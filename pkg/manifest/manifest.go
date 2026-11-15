package manifest

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

type Manifest struct {
	Name          string `yaml:"name"`
	Provider      string `yaml:"provider"`
	Region        string `yaml:"region"`
	InstanceClass string `yaml:"instanceClass"`
	InstanceType  string `yaml:"instanceType"`
	BaseImage     string `yaml:"baseImage"`
	// DiskSizeGb overrides the boot disk size in GB. 0 (default) uses the
	// image's own size. Must be >= the image's minimum on both clouds.
	DiskSizeGb int `yaml:"diskSizeGb"`
	// PublicIP controls whether a public IP is assigned. Nil means false when a
	// relay is set (the VM is reached through the relay, and gets out through
	// the subnet's NAT) and true when it is not (direct SSH is the only way in).
	// Set true with a relay only where the subnet has no NAT: the address then
	// carries outbound traffic, and sshd still listens on localhost only.
	PublicIP *bool             `yaml:"publicIp"`
	Relay    RelayConfig       `yaml:"relay"`
	Reclaim  ReclaimPolicy     `yaml:"reclaim"`
	Tags     map[string]string `yaml:"tags"`
	AWS      AWSConfig         `yaml:"aws"`
	GCP      GCPConfig         `yaml:"gcp"`
	// Sudo controls the sudo configuration of the runlease-agent user.
	// "locked" (default) leaves the existing locked-down behaviour: NOPASSWD
	// only for /usr/local/bin/runlease-terminate. "passwordless" grants
	// NOPASSWD: ALL. Empty string means "locked".
	Sudo SudoMode `yaml:"sudo"`
	// Setup is a list of scripts to run as root after the runlease bootstrap
	// completes. Each step is either an inline `run:` script or a `file:`
	// pointing at a local file that will be inlined at launch time.
	Setup []SetupStep `yaml:"setup"`
	// Forwards is the declarative version of `runlease forward -L …`. Applied
	// by `runlease up --attach` and by `runlease forward --all <name>`.
	Forwards []ForwardSpec `yaml:"forwards"`
}

// ForwardSpec declares one port forward attached to the instance.
// Direction is "local" (default, == ssh -L) or "remote" (== ssh -R).
type ForwardSpec struct {
	Name      string `yaml:"name"`
	Direction string `yaml:"direction"` // "local" (default) or "remote"
	Bind      string `yaml:"bind"`      // local listen address; "" → 127.0.0.1
	Local     int    `yaml:"local"`     // local port (mandatory for direction=local)
	Remote    int    `yaml:"remote"`    // remote port (mandatory for direction=local)
	Host      string `yaml:"host"`      // remote host on the instance side; default "localhost"
}

// SetupStep is one entry in manifest.setup. Exactly one of Run or File must
// be set. File is resolved relative to the manifest directory at load time.
type SetupStep struct {
	Name string `yaml:"name"`
	Run  string `yaml:"run"`
	File string `yaml:"file"`
}

// SudoMode is the privilege level granted to the runlease-agent user.
type SudoMode string

const (
	SudoLocked       SudoMode = "locked"
	SudoPasswordless SudoMode = "passwordless"
)

func (s SudoMode) IsPasswordless() bool { return s == SudoPasswordless }

// AWSConfig holds AWS-specific instance configuration.
type AWSConfig struct {
	SecurityGroupIDs []string `yaml:"securityGroupIDs"`
	SubnetID         string   `yaml:"subnetID"`
	InstanceProfile  string   `yaml:"instanceProfile"`
}

// GCPConfig holds GCP-specific instance configuration.
type GCPConfig struct {
	NetworkTags    []string `yaml:"networkTags"`
	ServiceAccount string   `yaml:"serviceAccount"`
	Network        string   `yaml:"network"`    // VPC network name or self-link; empty → GCP "default"
	Subnetwork     string   `yaml:"subnetwork"` // subnetwork name or self-link; required for custom-mode VPCs
}

// WantsPublicIP returns the effective public IP setting. Unset, it is false in
// relay mode and true otherwise: see the PublicIP field.
func (m *Manifest) WantsPublicIP() bool {
	if m.PublicIP != nil {
		return *m.PublicIP
	}
	return m.Relay.Addr == ""
}

// PublicIPExplicit reports whether the manifest sets publicIp itself, as
// opposed to taking the default.
func (m *Manifest) PublicIPExplicit() bool { return m.PublicIP != nil }

// RelayConfig enables the runlease relay tunnel. When set, launched instances
// download runlease-agent from the relay's built-in HTTP endpoint and connect
// outbound, so you can reach them without a public IP or open inbound firewall rules.
type RelayConfig struct {
	Addr string `yaml:"addr"` // host:port of the relay TCP server (e.g. "relay.example.com:7777")
	// AgentSHA256 pins the agent binary the relay serves. Release builds of the
	// CLI carry the digest of the matching agent, so this is only needed for a
	// self-built relay: set it to the SHA-256 of your agent binary, or to
	// "unverified" to skip the check on purpose.
	AgentSHA256 string `yaml:"agentSha256,omitempty"`
}

type ReclaimPolicy struct {
	TTL string `yaml:"ttl"`
	// SelfTermination arms a timer on the VM that deletes it at the end of
	// the TTL. Unset means true: a lease that nothing enforces is not a lease.
	// Set false only if something else (the reclaim daemon) will stop the VM.
	SelfTermination *bool `yaml:"selfTermination"`
}

// SelfTerminates reports whether the VM deletes itself at the end of its TTL.
func (r *ReclaimPolicy) SelfTerminates() bool {
	return r.SelfTermination == nil || *r.SelfTermination
}

func (r *ReclaimPolicy) TTLDuration() (time.Duration, error) {
	if r.TTL == "" {
		return 0, nil
	}
	return time.ParseDuration(r.TTL)
}

func Load(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading manifest: %w", err)
	}
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing manifest: %w", err)
	}
	baseDir := filepath.Dir(path)
	if err := m.resolveSetupFiles(baseDir); err != nil {
		return nil, err
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// resolveSetupFiles inlines any setup step that uses `file:` so that the rest
// of the pipeline only ever sees inline `run:` content. baseDir is the directory
// of the manifest file; relative file: paths are resolved against it.
func (m *Manifest) resolveSetupFiles(baseDir string) error {
	for i := range m.Setup {
		s := &m.Setup[i]
		if s.File == "" {
			continue
		}
		if s.Run != "" {
			return fmt.Errorf("manifest.setup[%d]: `run` and `file` are mutually exclusive", i)
		}
		p := s.File
		if !filepath.IsAbs(p) {
			p = filepath.Join(baseDir, p)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("manifest.setup[%d]: reading file %s: %w", i, s.File, err)
		}
		s.Run = string(data)
		s.File = ""
	}
	return nil
}

func (m *Manifest) Validate() error {
	if m.Name == "" {
		return fmt.Errorf("manifest.name is required")
	}
	if m.Provider == "" {
		return fmt.Errorf("manifest.provider is required")
	}
	if m.Region == "" {
		return fmt.Errorf("manifest.region is required")
	}
	if m.InstanceType == "" && m.InstanceClass == "" {
		return fmt.Errorf("one of manifest.instanceType or manifest.instanceClass is required")
	}
	if m.InstanceType != "" && m.InstanceClass != "" {
		return fmt.Errorf("manifest.instanceType and manifest.instanceClass are mutually exclusive")
	}
	if m.BaseImage == "" && m.Provider != "aws" && m.Provider != "gcp" {
		return fmt.Errorf("manifest.baseImage is required")
	}
	if m.DiskSizeGb < 0 {
		return fmt.Errorf("manifest.diskSizeGb must be >= 0 (0 = image default), got %d", m.DiskSizeGb)
	}
	if m.Reclaim.TTL != "" {
		d, err := m.Reclaim.TTLDuration()
		if err != nil {
			return fmt.Errorf("manifest.reclaim.ttl invalid: %w", err)
		}
		if d < time.Second {
			return fmt.Errorf("manifest.reclaim.ttl must be at least 1s, got %q", m.Reclaim.TTL)
		}
	}
	switch m.Sudo {
	case "", SudoLocked, SudoPasswordless:
	default:
		return fmt.Errorf("manifest.sudo invalid: %q (allowed: locked, passwordless)", m.Sudo)
	}
	for i, s := range m.Setup {
		if s.Run == "" && s.File == "" {
			return fmt.Errorf("manifest.setup[%d]: either `run` or `file` is required", i)
		}
		if s.Run != "" && s.File != "" {
			return fmt.Errorf("manifest.setup[%d]: `run` and `file` are mutually exclusive", i)
		}
	}
	for i, f := range m.Forwards {
		switch f.Direction {
		case "", "local", "remote":
		default:
			return fmt.Errorf("manifest.forwards[%d].direction invalid: %q (allowed: local, remote)", i, f.Direction)
		}
		if f.Local <= 0 || f.Remote <= 0 {
			return fmt.Errorf("manifest.forwards[%d]: both `local` and `remote` ports are required", i)
		}
	}
	return nil
}
