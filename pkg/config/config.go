package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Relay    RelayConfig    `yaml:"relay,omitempty"`
	Defaults InstanceConfig `yaml:"defaults,omitempty"`
}

type RelayConfig struct {
	Addr string `yaml:"addr,omitempty"`
}

type InstanceConfig struct {
	Provider string `yaml:"provider,omitempty"`
	Size     string `yaml:"size,omitempty"`
	TTL      string `yaml:"ttl,omitempty"`
	Region   string `yaml:"region,omitempty"`
}

// Load reads the config file. Returns a zero Config (not an error) if the
// file does not exist.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	return &c, nil
}

func Save(path string, c *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}
	return os.WriteFile(path, data, 0600)
}

// Set applies key=value to c where key is dot-notation.
// Supported keys: relay.addr, defaults.provider, defaults.size, defaults.ttl, defaults.region
func (c *Config) Set(key, value string) error {
	switch key {
	case "relay.addr":
		c.Relay.Addr = value
	case "defaults.provider":
		c.Defaults.Provider = value
	case "defaults.size":
		c.Defaults.Size = value
	case "defaults.ttl":
		c.Defaults.TTL = value
	case "defaults.region":
		c.Defaults.Region = value
	default:
		return fmt.Errorf("unknown key %q — valid keys: relay.addr, defaults.provider, defaults.size, defaults.ttl, defaults.region", key)
	}
	return nil
}
