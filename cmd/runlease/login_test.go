package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/marshal-org/runlease/pkg/relay"
	"github.com/marshal-org/runlease/pkg/signin"
)

// testRelay runs a real relay whose verifier treats the bearer token as the
// caller's email, with a two-machine free tier.
func testRelay(t *testing.T) string {
	t.Helper()
	tiers, err := relay.ParseTiers([]byte("default: free\ntiers:\n  free:\n    maxSlots: 2\n    slotTTL: 1h\n"))
	if err != nil {
		t.Fatal(err)
	}
	srv := relay.New(slog.New(slog.NewTextHandler(io.Discard, nil)), relay.Config{
		Tiers:   tiers,
		Version: "test",
		Auth: relay.AuthConfig{
			GoogleClientIDs: []string{"cid"},
			Verifier: func(_ context.Context, raw string) (relay.Identity, error) {
				if !strings.Contains(raw, "@") {
					return relay.Identity{}, fmt.Errorf("invalid token")
				}
				return relay.Identity{Email: raw}, nil
			},
		},
	})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return "ws" + strings.TrimPrefix(ts.URL, "http")
}

func slotToken(n int) string { return fmt.Sprintf("%064x", n) }

// A saved sign-in is what the CLI presents to the relay, and the relay's
// limits, account view and release all work end to end through the CLI's own
// functions.
func TestSignedInSession_registerLimitWhoamiRelease(t *testing.T) {
	t.Setenv("RUNLEASE_HOME", t.TempDir())
	addr := testRelay(t)
	ctx := context.Background()

	if err := signinStore().Put(sessionKey(addr), signin.Session{
		ClientID: "cid", Email: "alice@example.com", IDToken: "alice@example.com",
		Expiry: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	d, err := fetchDiscovery(ctx, addr)
	if err != nil {
		t.Fatalf("discovery: %v", err)
	}
	if !d.Auth.Required || d.Auth.Google == nil || d.Auth.Google.ClientID != "cid" || d.DefaultTier == nil || d.DefaultTier.MaxSlots != 2 {
		t.Fatalf("discovery document: %+v", d)
	}

	for i := 0; i < 2; i++ {
		if err := registerRelayToken(ctx, addr, slotToken(i)); err != nil {
			t.Fatalf("register %d: %v", i, err)
		}
	}
	err = registerRelayToken(ctx, addr, slotToken(2))
	if err == nil || !strings.Contains(err.Error(), "limit") || !strings.Contains(err.Error(), "runlease down") {
		t.Fatalf("third machine: want a limit error that says what to do, got %v", err)
	}

	a, err := fetchAccount(ctx, addr)
	if err != nil {
		t.Fatalf("whoami: %v", err)
	}
	if a.Email != "alice@example.com" || a.Tier != "free" || a.Slots.Used != 2 || a.Slots.Max != 2 {
		t.Fatalf("account: %+v", a)
	}

	if err := releaseRelaySlot(ctx, addr, slotToken(0)); err != nil {
		t.Fatalf("release: %v", err)
	}
	if a, _ = fetchAccount(ctx, addr); a == nil || a.Slots.Used != 1 {
		t.Fatalf("after release: %+v", a)
	}
	if err := registerRelayToken(ctx, addr, slotToken(2)); err != nil {
		t.Fatalf("register after release: %v", err)
	}
}

func TestSessionKey(t *testing.T) {
	cases := map[string]string{
		"wss://relay.example.com":  "https://relay.example.com",
		"wss://relay.example.com/": "https://relay.example.com",
		"ws://localhost:7777":      "http://localhost:7777",
		"localhost:7777":           "http://localhost:7777",
	}
	for in, want := range cases {
		if got := sessionKey(in); got != want {
			t.Errorf("sessionKey(%q) = %q, want %q", in, got, want)
		}
	}
}
