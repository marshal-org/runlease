package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	rlconfig "github.com/marshal-org/runlease/pkg/config"
	"github.com/marshal-org/runlease/pkg/provider"
)

// jsonMode turns on --output json for one test.
func jsonMode(t *testing.T) {
	t.Helper()
	outputMode = "json"
	t.Cleanup(func() { outputMode = "" })
}

// capture points a command's output at buffers and returns them.
func capture(cmd *cobra.Command) (stdout, stderr *bytes.Buffer) {
	stdout, stderr = &bytes.Buffer{}, &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetContext(context.Background())
	return stdout, stderr
}

// decode fails the test unless out is exactly one JSON document.
func decode(t *testing.T, out *bytes.Buffer, v any) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(out.Bytes()))
	if err := dec.Decode(v); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, out.String())
	}
	if dec.More() {
		t.Fatalf("stdout holds more than one JSON document:\n%s", out.String())
	}
}

func TestDown_JSON(t *testing.T) {
	jsonMode(t)
	reg := untrackedHarness(t, &cloudFake{})
	if err := reg.Add(trackedEntry(t, "box", "rl-box-aaaaaa")); err != nil {
		t.Fatal(err)
	}
	stdout, _ := capture(downCmd)

	if err := runDown(downCmd, []string{"box"}); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("without --yes: err = %v, want it to ask for --yes", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("a refused run must print nothing on stdout: %q", stdout.String())
	}

	downYes = true
	t.Cleanup(func() { downYes = false })
	if err := runDown(downCmd, []string{"box"}); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	decode(t, stdout, &got)
	want := map[string]any{"name": "box", "id": "rl-box-aaaaaa", "provider": "gcp", "region": "europe-west1-b", "terminated": true, "alreadyGone": false}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("keys = %v, want exactly %v", got, want)
	}
	if reg2, _ := loadRegistry(); len(reg2.List()) != 0 {
		t.Error("the entry should be gone from the registry")
	}
}

// With no name, JSON output cannot print a pick-list, so it says what it needs.
func TestDown_JSONNeedsAName(t *testing.T) {
	jsonMode(t)
	untrackedHarness(t, &cloudFake{})
	stdout, _ := capture(downCmd)
	downYes = true
	t.Cleanup(func() { downYes = false })
	if err := runDown(downCmd, nil); err == nil || stdout.Len() != 0 {
		t.Errorf("err = %v, stdout = %q; want an error and nothing printed", err, stdout.String())
	}
}

func TestDownLeaked_JSON(t *testing.T) {
	jsonMode(t)
	cloud := &cloudFake{
		managed: []provider.ManagedInstance{
			managed("rl-leak-bbbbbb", provider.StatusStopped, -time.Hour),
			managed("rl-back-ffffff", provider.StatusStopped, -time.Hour),
			managed("rl-colleague-dddddd", provider.StatusRunning, time.Hour),
		},
		stateNow: map[provider.InstanceID]provider.InstanceStatus{"rl-back-ffffff": provider.StatusRunning},
	}
	reg := untrackedHarness(t, cloud)
	if err := reg.Add(trackedEntry(t, "mine", "rl-mine-eeeeee")); err != nil {
		t.Fatal(err)
	}
	stdout, _ := capture(downCmd)
	downYes = true
	t.Cleanup(func() { downYes = false })
	if err := runDownLeaked(downCmd, reg); err != nil {
		t.Fatal(err)
	}
	var got leakedJSON
	decode(t, stdout, &got)
	if len(got.Scopes) != 1 || got.Scopes[0] != "gcp project proj (all zones)" {
		t.Errorf("scopes = %v", got.Scopes)
	}
	if len(got.Deleted) != 1 || got.Deleted[0].ID != "rl-leak-bbbbbb" || got.Deleted[0].Project != "proj" || got.Deleted[0].Provider != "gcp" {
		t.Errorf("deleted = %+v", got.Deleted)
	}
	if len(got.Kept) != 1 || got.Kept[0].ID != "rl-back-ffffff" || !strings.Contains(got.Kept[0].Reason, "running") {
		t.Errorf("kept = %+v", got.Kept)
	}
	for _, key := range []string{`"scopes"`, `"deleted"`, `"kept"`, `"failed": []`, `"notes": []`} {
		if !strings.Contains(stdout.String(), key) {
			t.Errorf("output has no %s:\n%s", key, stdout.String())
		}
	}
}

func TestDownLeaked_JSONNothingFound(t *testing.T) {
	jsonMode(t)
	reg := untrackedHarness(t, &cloudFake{})
	if err := reg.Add(trackedEntry(t, "mine", "rl-mine-eeeeee")); err != nil {
		t.Fatal(err)
	}
	stdout, _ := capture(downCmd)
	if err := runDownLeaked(downCmd, reg); err != nil {
		t.Fatal(err)
	}
	var got leakedJSON
	decode(t, stdout, &got)
	if len(got.Deleted) != 0 || !strings.Contains(stdout.String(), `"deleted": []`) {
		t.Errorf("want an empty deleted array:\n%s", stdout.String())
	}
}

func TestSync_JSON(t *testing.T) {
	jsonMode(t)
	cloud := &cloudFake{
		managed: []provider.ManagedInstance{
			managed("rl-up-aaaaaa", provider.StatusRunning, time.Hour),
			managed("rl-off-bbbbbb", provider.StatusStopped, time.Hour),
		},
	}
	reg := untrackedHarness(t, cloud)
	for name, id := range map[string]string{"up": "rl-up-aaaaaa", "off": "rl-off-bbbbbb", "gone": "rl-gone-cccccc"} {
		e := trackedEntry(t, name, id)
		e.Handle.State = provider.StatusPending
		if err := reg.Add(e); err != nil {
			t.Fatal(err)
		}
	}
	stdout, _ := capture(syncCmd)
	if err := runSync(syncCmd, nil); err != nil {
		t.Fatal(err)
	}
	var rows []syncJSON
	decode(t, stdout, &rows)
	got := map[string]syncJSON{}
	for _, r := range rows {
		got[r.Name] = r
	}
	for name, want := range map[string]syncJSON{
		"up":   {State: "running", Action: "updated"},
		"off":  {State: "stopped", Action: "removed"}, // powered off: deleted, then forgotten
		"gone": {State: "terminated", Action: "removed"},
	} {
		r := got[name]
		if r.State != want.State || r.Action != want.Action || r.PreviousState != "pending" || r.Provider != "gcp" || r.ID == "" || r.Region == "" {
			t.Errorf("%s = %+v, want state %s action %s", name, r, want.State, want.Action)
		}
	}
	if len(cloud.terminated) != 1 || cloud.terminated[0] != "rl-off-bbbbbb" {
		t.Errorf("terminated %v, want only the stopped machine", cloud.terminated)
	}

	// A second run finds one machine, unchanged.
	stdout.Reset()
	if err := runSync(syncCmd, nil); err != nil {
		t.Fatal(err)
	}
	rows = nil
	decode(t, stdout, &rows)
	if len(rows) != 1 || rows[0].Action != "unchanged" {
		t.Errorf("second run = %+v", rows)
	}
}

func TestSync_JSONEmptyIsAnArray(t *testing.T) {
	jsonMode(t)
	untrackedHarness(t, &cloudFake{})
	stdout, _ := capture(syncCmd)
	if err := runSync(syncCmd, nil); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(stdout.String()) != "[]" {
		t.Errorf("got %q, want []", stdout.String())
	}
}

// The human output of sync is what it was before JSON was added.
func TestSync_humanOutputUnchanged(t *testing.T) {
	cloud := &cloudFake{managed: []provider.ManagedInstance{managed("rl-up-aaaaaa", provider.StatusRunning, time.Hour)}}
	reg := untrackedHarness(t, cloud)
	e := trackedEntry(t, "up", "rl-up-aaaaaa")
	e.Handle.State = provider.StatusPending
	if err := reg.Add(e); err != nil {
		t.Fatal(err)
	}
	stdout, _ := capture(syncCmd)
	if err := runSync(syncCmd, nil); err != nil {
		t.Fatal(err)
	}
	if want := "up                      pending → running\n"; stdout.String() != want {
		t.Errorf("got %q, want %q", stdout.String(), want)
	}
}

func TestVersionCatalogConfig_JSON(t *testing.T) {
	jsonMode(t)
	t.Setenv("RUNLEASE_HOME", t.TempDir())
	if err := rlconfig.Save(defaultConfigPath(), &rlconfig.Config{
		Relay: rlconfig.RelayConfig{Addr: "wss://relay.example.com"}, Defaults: rlconfig.InstanceConfig{Provider: "aws", Region: "eu-north-1"},
	}); err != nil {
		t.Fatal(err)
	}

	stdout, _ := capture(versionCmd)
	if err := versionCmd.RunE(versionCmd, nil); err != nil {
		t.Fatal(err)
	}
	var v map[string]string
	decode(t, stdout, &v)
	if _, ok := v["agentSha256"]; v["version"] != version || !ok {
		t.Errorf("version = %v", v)
	}

	stdout, _ = capture(catalogCmd)
	if err := runCatalog(catalogCmd, nil); err != nil {
		t.Fatal(err)
	}
	var classes []struct {
		Class, Provider string
		InstanceTypes   []string
	}
	decode(t, stdout, &classes)
	if len(classes) == 0 || classes[0].Class == "" || classes[0].Provider == "" || len(classes[0].InstanceTypes) == 0 {
		t.Errorf("catalog = %+v", classes)
	}

	stdout, _ = capture(configShowCmd)
	if err := configShowCmd.RunE(configShowCmd, nil); err != nil {
		t.Fatal(err)
	}
	var c struct {
		Path, Relay string
		Defaults    map[string]string
	}
	decode(t, stdout, &c)
	if c.Relay != "wss://relay.example.com" || c.Defaults["provider"] != "aws" || c.Defaults["region"] != "eu-north-1" || c.Path == "" {
		t.Errorf("config = %+v", c)
	}
	if _, ok := c.Defaults["ttl"]; !ok {
		t.Errorf("an unset default should still be a key: %v", c.Defaults)
	}
}

// whoami and snooze had JSON already; this pins the whoami shape.
func TestRelayAccount_JSONShape(t *testing.T) {
	a := relayAccount{Email: "me@example.com", Tier: "free", SlotTTL: "8h", IdleTTL: "30m"}
	a.Slots.Used, a.Slots.Max = 1, 2
	b, _ := json.Marshal(a)
	if want := `{"email":"me@example.com","tier":"free","slots":{"used":1,"max":2},"slotTTL":"8h","idleTTL":"30m"}`; string(b) != want {
		t.Errorf("got  %s\nwant %s", b, want)
	}
}
