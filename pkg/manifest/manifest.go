package manifest

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

type Manifest struct {
	Name          string            `yaml:"name"`
	Provider      string            `yaml:"provider"`
	Region        string            `yaml:"region"`
	InstanceClass string            `yaml:"instanceClass"`
	InstanceType  string            `yaml:"instanceType"`
	BaseImage     string            `yaml:"baseImage"`
	// PublicIP controls whether a public IP is assigned. Nil = auto: true when
	// no relay is configured (direct SSH needs it), false when relay is set.
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
}

// WantsPublicIP returns the effective public IP setting (default true).
func (m *Manifest) WantsPublicIP() bool {
	if m.PublicIP != nil {
		return *m.PublicIP
	}
	return true
}

// RelayConfig enables the runlease relay tunnel. When set, launched instances
// download runlease-agent from the relay's built-in HTTP endpoint and connect
// outbound, so you can reach them without a public IP or open inbound firewall rules.
type RelayConfig struct {
	Addr string `yaml:"addr"` // host:port of the relay TCP server (e.g. "relay.example.com:7777")
}

type ReclaimPolicy struct {
	TTL             string `yaml:"ttl"`
	SelfTermination bool   `yaml:"selfTermination"`
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
	if m.Reclaim.TTL != "" {
		if _, err := m.Reclaim.TTLDuration(); err != nil {
			return fmt.Errorf("manifest.reclaim.ttl invalid: %w", err)
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
	return nil
}
