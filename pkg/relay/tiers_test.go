package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeVerifier treats the bearer token as the caller's email, so tests can
// act as any identity without a real identity provider.
func fakeVerifier(_ context.Context, raw string) (Identity, error) {
	if !strings.Contains(raw, "@") {
		return Identity{}, fmt.Errorf("invalid token")
	}
	return Identity{Email: raw}, nil
}

const testTiersYAML = `
default: free
tiers:
  free:
    maxSlots: 2
    slotTTL: 1h
    idleTTL: 150ms
  team:
    maxSlots: 5
assign:
  emails:
    Boss@Example.com: team
  domains:
    paid.example: team
`

func tieredServer(t *testing.T, yamlText string) string {
	t.Helper()
	tiers, err := ParseTiers([]byte(yamlText))
	if err != nil {
		t.Fatalf("ParseTiers: %v", err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ts := httptest.NewServer(New(log, Config{
		Tiers:              tiers,
		GoogleClientSecret: "not-a-secret",
		Version:            "test",
		Auth:               AuthConfig{Verifier: fakeVerifier, GoogleClientIDs: []string{"client-id.apps.example"}},
	}))
	t.Cleanup(ts.Close)
	return ts.URL
}

func tok(name string) string { return name + "-0123456789abcdef0123456789abcdef" }

// registerAs opens a slot as the given identity and returns the relay's reply.
func registerAs(t *testing.T, serverURL, email, token string) map[string]string {
	t.Helper()
	h := http.Header{}
	if email != "" {
		h.Set("Authorization", "Bearer "+email)
	}
	c, _, err := websocket.DefaultDialer.Dial(wsURL(serverURL), h)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	sendWS(t, c, map[string]string{"role": "register", "token": token})
	return recvWS(t, c)
}

func release(t *testing.T, serverURL, token string) {
	t.Helper()
	c := dialWS(t, serverURL)
	defer c.Close()
	sendWS(t, c, map[string]string{"role": "release", "token": token})
	if resp := recvWS(t, c); resp["status"] != "ok" {
		t.Fatalf("release: %v", resp)
	}
}

func me(t *testing.T, serverURL, email string) map[string]any {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, serverURL+"/me", nil)
	req.Header.Set("Authorization", "Bearer "+email)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /me: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /me: status %d", resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode /me: %v", err)
	}
	return out
}

func used(t *testing.T, serverURL, email string) int {
	t.Helper()
	return int(me(t, serverURL, email)["slots"].(map[string]any)["used"].(float64))
}

func TestTiers_perIdentityLimit(t *testing.T) {
	// A long idle timeout, so only the limit is under test here.
	srv := tieredServer(t, strings.Replace(testTiersYAML, "idleTTL: 150ms", "idleTTL: 1h", 1))

	for i := 0; i < 2; i++ {
		if resp := registerAs(t, srv, "alice@free.example", tok(fmt.Sprintf("alice-%d", i))); resp["status"] != "ok" {
			t.Fatalf("slot %d: %v", i, resp)
		}
	}
	resp := registerAs(t, srv, "alice@free.example", tok("alice-2"))
	if resp["status"] != "limit" || !strings.Contains(resp["message"], "2 machines") {
		t.Fatalf("third slot: want limit, got %v", resp)
	}

	// Another identity is not affected by alice's usage.
	if resp := registerAs(t, srv, "bob@free.example", tok("bob-0")); resp["status"] != "ok" {
		t.Fatalf("bob: %v", resp)
	}
	// The same person with different capitalisation is the same identity.
	if resp := registerAs(t, srv, "ALICE@free.example", tok("alice-3")); resp["status"] != "limit" {
		t.Fatalf("case variant must share the limit, got %v", resp)
	}
}

func TestTiers_releaseFreesThePlace(t *testing.T) {
	srv := tieredServer(t, strings.Replace(testTiersYAML, "idleTTL: 150ms", "idleTTL: 1h", 1))
	registerAs(t, srv, "alice@free.example", tok("a0"))
	registerAs(t, srv, "alice@free.example", tok("a1"))
	if got := used(t, srv, "alice@free.example"); got != 2 {
		t.Fatalf("used = %d, want 2", got)
	}

	release(t, srv, tok("a0"))
	if got := used(t, srv, "alice@free.example"); got != 1 {
		t.Fatalf("used after release = %d, want 1", got)
	}
	if resp := registerAs(t, srv, "alice@free.example", tok("a2")); resp["status"] != "ok" {
		t.Fatalf("slot after release: %v", resp)
	}
	// Releasing twice, or releasing something unknown, is not an error.
	release(t, srv, tok("a0"))
	release(t, srv, tok("never-registered"))

	// A released slot no longer accepts clients.
	c := dialWS(t, srv)
	defer c.Close()
	sendWS(t, c, map[string]string{"role": "client", "token": tok("a0")})
	if resp := recvWS(t, c); resp["status"] != "unauthorized" {
		t.Fatalf("client on released slot: %v", resp)
	}
}

func TestTiers_idleSlotIsReleased(t *testing.T) {
	srv := tieredServer(t, testTiersYAML) // idleTTL: 150ms
	registerAs(t, srv, "alice@free.example", tok("idle-0"))
	registerAs(t, srv, "alice@free.example", tok("idle-1"))

	deadline := time.Now().Add(3 * time.Second)
	for used(t, srv, "alice@free.example") != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("slots with no agent were not released; used = %d", used(t, srv, "alice@free.example"))
		}
		time.Sleep(25 * time.Millisecond)
	}
	if resp := registerAs(t, srv, "alice@free.example", tok("idle-2")); resp["status"] != "ok" {
		t.Fatalf("slot after idle release: %v", resp)
	}
}

func TestTiers_connectedAgentKeepsTheSlot(t *testing.T) {
	srv := tieredServer(t, testTiersYAML) // idleTTL: 150ms
	registerAs(t, srv, "alice@free.example", tok("busy"))
	stop := fakeAgent(t, srv, tok("busy"), echoStream)
	defer stop()

	time.Sleep(450 * time.Millisecond) // three idle periods
	if got := used(t, srv, "alice@free.example"); got != 1 {
		t.Fatalf("slot with a connected agent was released; used = %d", got)
	}
}

func TestTiers_assignment(t *testing.T) {
	srv := tieredServer(t, testTiersYAML)
	cases := map[string]string{
		"someone@free.example": "free",
		"boss@example.com":     "team", // email assignment, case-insensitive
		"dev@paid.example":     "team", // domain assignment
	}
	for email, want := range cases {
		if got := me(t, srv, email)["tier"]; got != want {
			t.Errorf("%s: tier = %v, want %s", email, got, want)
		}
	}
	// The team tier really has the higher limit.
	for i := 0; i < 5; i++ {
		if resp := registerAs(t, srv, "dev@paid.example", tok(fmt.Sprintf("team-%d", i))); resp["status"] != "ok" {
			t.Fatalf("team slot %d: %v", i, resp)
		}
	}
	if resp := registerAs(t, srv, "dev@paid.example", tok("team-5")); resp["status"] != "limit" {
		t.Fatalf("sixth team slot: %v", resp)
	}
}

func TestAuth_registerNeedsAnIdentity(t *testing.T) {
	srv := tieredServer(t, testTiersYAML)
	if resp := registerAs(t, srv, "", tok("anon")); resp["status"] != "unauthorized" {
		t.Fatalf("no bearer: %v", resp)
	}
	if resp := registerAs(t, srv, "not-an-email", tok("bad")); resp["status"] != "unauthorized" {
		t.Fatalf("bad bearer: %v", resp)
	}
	req, _ := http.NewRequest(http.MethodGet, srv+"/me", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("/me without a token: status %d", resp.StatusCode)
	}
}

func TestDiscovery(t *testing.T) {
	srv := tieredServer(t, testTiersYAML)
	resp, err := http.Get(srv + "/.well-known/runlease-relay")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var doc struct {
		Version string `json:"version"`
		Auth    struct {
			Required bool `json:"required"`
			Google   struct {
				ClientID     string `json:"clientId"`
				ClientSecret string `json:"clientSecret"`
			} `json:"google"`
		} `json:"auth"`
		DefaultTier struct {
			Name     string `json:"name"`
			MaxSlots int    `json:"maxSlots"`
		} `json:"defaultTier"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if !doc.Auth.Required || doc.Auth.Google.ClientID != "client-id.apps.example" || doc.Auth.Google.ClientSecret != "not-a-secret" {
		t.Errorf("auth section wrong: %+v", doc.Auth)
	}
	if doc.DefaultTier.Name != "free" || doc.DefaultTier.MaxSlots != 2 {
		t.Errorf("default tier wrong: %+v", doc.DefaultTier)
	}
}

func TestParseTiers_rejectsMistakes(t *testing.T) {
	bad := map[string]string{
		"no tiers":           "default: free\n",
		"no default":         "tiers:\n  free: {maxSlots: 1}\n",
		"undefined default":  "default: pro\ntiers:\n  free: {maxSlots: 1}\n",
		"undefined assigned": "default: free\ntiers:\n  free: {maxSlots: 1}\nassign:\n  emails:\n    a@b.c: pro\n",
		"negative limit":     "default: free\ntiers:\n  free: {maxSlots: -1}\n",
		"misspelt field":     "default: free\ntiers:\n  free: {maxSlot: 1}\n",
		"unparseable ttl":    "default: free\ntiers:\n  free: {slotTTL: soon}\n",
		"negative ttl":       "default: free\ntiers:\n  free: {idleTTL: -5m}\n",
	}
	for name, text := range bad {
		if _, err := ParseTiers([]byte(text)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestTiers_nilMeansNoPerIdentityLimit(t *testing.T) {
	var none *Tiers
	name, tier := none.For("anyone@example.com")
	if name != "" || tier.MaxSlots != 0 || tier.slotTTL() != tokenTTL {
		t.Errorf("nil tiers: got %q %+v", name, tier)
	}
}
