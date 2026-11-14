package relay

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a time.Duration that reads from YAML as "8h", "15m", and so on.
type Duration time.Duration

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	if v < 0 {
		return fmt.Errorf("line %d: duration must not be negative", n.Line)
	}
	*d = Duration(v)
	return nil
}

func (d Duration) String() string { return time.Duration(d).String() }

// Tier is a set of limits applied to every identity assigned to it.
type Tier struct {
	// MaxSlots is how many slots (machines) one identity may hold at once.
	// Zero means no per-identity limit.
	MaxSlots int `yaml:"maxSlots"`
	// SlotTTL is how long a slot lives. Zero means the relay default (24h).
	SlotTTL Duration `yaml:"slotTTL"`
	// IdleTTL releases a slot whose agent has been absent this long: the
	// machine is gone, or never came up. Zero means never.
	IdleTTL Duration `yaml:"idleTTL"`
}

// Tiers maps identities to limits. The zero value, and a nil *Tiers, put
// everyone in one unnamed tier with no per-identity limits.
type Tiers struct {
	// Default is the tier for an identity that no assignment matches.
	Default string          `yaml:"default"`
	Tiers   map[string]Tier `yaml:"tiers"`
	Assign  struct {
		Emails  map[string]string `yaml:"emails"`
		Domains map[string]string `yaml:"domains"`
	} `yaml:"assign"`
}

// LoadTiers reads and validates a tiers file.
func LoadTiers(path string) (*Tiers, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading tiers file: %w", err)
	}
	return ParseTiers(raw)
}

// ParseTiers parses and validates tiers YAML.
func ParseTiers(raw []byte) (*Tiers, error) {
	var t Tiers
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true) // a misspelt limit must not silently mean "unlimited"
	if err := dec.Decode(&t); err != nil {
		return nil, fmt.Errorf("parsing tiers file: %w", err)
	}
	if err := t.validate(); err != nil {
		return nil, err
	}
	return &t, nil
}

func (t *Tiers) validate() error {
	if len(t.Tiers) == 0 {
		return fmt.Errorf("tiers file defines no tiers")
	}
	if t.Default == "" {
		return fmt.Errorf("tiers file needs a default tier")
	}
	if _, ok := t.Tiers[t.Default]; !ok {
		return fmt.Errorf("default tier %q is not defined", t.Default)
	}
	for name, tier := range t.Tiers {
		if tier.MaxSlots < 0 {
			return fmt.Errorf("tier %q: maxSlots must not be negative", name)
		}
	}
	lower := func(in map[string]string, what string) (map[string]string, error) {
		out := make(map[string]string, len(in))
		for k, tier := range in {
			if _, ok := t.Tiers[tier]; !ok {
				return nil, fmt.Errorf("%s %q is assigned to undefined tier %q", what, k, tier)
			}
			out[strings.ToLower(strings.TrimSpace(k))] = tier
		}
		return out, nil
	}
	var err error
	if t.Assign.Emails, err = lower(t.Assign.Emails, "email"); err != nil {
		return err
	}
	if t.Assign.Domains, err = lower(t.Assign.Domains, "domain"); err != nil {
		return err
	}
	return nil
}

// For returns the tier name and limits for an email. An exact email
// assignment wins over a domain assignment, which wins over the default.
func (t *Tiers) For(email string) (string, Tier) {
	if t == nil || len(t.Tiers) == 0 {
		return "", Tier{}
	}
	email = strings.ToLower(email)
	if name, ok := t.Assign.Emails[email]; ok {
		return name, t.Tiers[name]
	}
	if i := strings.LastIndex(email, "@"); i >= 0 {
		if name, ok := t.Assign.Domains[email[i+1:]]; ok {
			return name, t.Tiers[name]
		}
	}
	return t.Default, t.Tiers[t.Default]
}

func (t Tier) slotTTL() time.Duration {
	if t.SlotTTL > 0 {
		return time.Duration(t.SlotTTL)
	}
	return tokenTTL
}
