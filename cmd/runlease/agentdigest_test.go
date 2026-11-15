package main

import (
	"strings"
	"testing"
)

func TestResolveAgentSHA256(t *testing.T) {
	a := strings.Repeat("a", 64)
	b := strings.Repeat("b", 64)
	cases := []struct {
		name, manifest, build, want string
		wantErr                     bool
	}{
		{"dev build, nothing set", "", "", "", false},
		{"release build", "", a, a, false},
		{"manifest overrides build", b, a, b, false},
		{"manifest upper case is normalised", strings.ToUpper(b), "", b, false},
		{"explicit opt-out beats build pin", "unverified", a, "", false},
		{"too short", "abc", "", "", true},
		{"not hex", strings.Repeat("z", 64), "", "", true},
		{"shell metacharacters", strings.Repeat("a", 60) + "';#\"", "", "", true},
	}
	for _, c := range cases {
		got, err := resolveAgentSHA256(c.manifest, c.build)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", c.name, err, c.wantErr)
		}
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
