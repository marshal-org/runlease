package manifest

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Manifest struct {
	Name             string            `yaml:"name"`
	Provider         string            `yaml:"provider"`
	Region           string            `yaml:"region"`
	InstanceClass    string            `yaml:"instanceClass"`
	InstanceType     string            `yaml:"instanceType"`
	BaseImage        string            `yaml:"baseImage"`
	SecurityGroupIDs []string          `yaml:"securityGroupIDs"`
	SubnetID         string            `yaml:"subnetID"`
	InstanceProfile  string            `yaml:"instanceProfile"`
	ServiceAccount   string            `yaml:"serviceAccount"`
	NetworkTags      []string          `yaml:"networkTags"`
	Relay            RelayConfig       `yaml:"relay"`
	Reclaim          ReclaimPolicy     `yaml:"reclaim"`
	Tags             map[string]string `yaml:"tags"`
	// PublicIP controls whether a public IP is assigned. Nil = auto: true when
	// no relay is configured (direct SSH needs it), false when relay is set.
	PublicIP *bool `yaml:"publicIp"`
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
	TTL string `yaml:"ttl"`
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
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
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
	if m.BaseImage == "" {
		return fmt.Errorf("manifest.baseImage is required")
	}
	if m.Reclaim.TTL != "" {
		if _, err := m.Reclaim.TTLDuration(); err != nil {
			return fmt.Errorf("manifest.reclaim.ttl invalid: %w", err)
		}
	}
	return nil
}
