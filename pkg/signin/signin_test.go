package signin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// fakeIDToken builds an unsigned token with the given claims. The client never
// verifies signatures (the relay does), so this is all the tests need.
func fakeIDToken(email string, exp time.Time) string {
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	return enc(map[string]string{"alg": "none"}) + "." +
		enc(map[string]any{"email": email, "exp": exp.Unix()}) + ".sig"
}

// fakeGoogle answers the device and token endpoints. pendingPolls is how many
// times the token endpoint says "authorization_pending" before it answers.
type fakeGoogle struct {
	t            *testing.T
	pendingPolls int32
	final        map[string]any // the answer after the pending polls
	polls        atomic.Int32
	refreshes    atomic.Int32
}

func (f *fakeGoogle) server() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/device", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm() //nolint:errcheck
		if r.Form.Get("client_id") != "cid" || r.Form.Get("scope") != "openid email" {
			f.t.Errorf("device request form: %v", r.Form)
		}
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"device_code": "dev-code", "user_code": "ABCD-EFGH",
			"verification_url": "https://example.test/device", "expires_in": 600, "interval": 1,
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm() //nolint:errcheck
		if r.Form.Get("client_id") != "cid" || r.Form.Get("client_secret") != "csecret" {
			f.t.Errorf("token request is missing client credentials: %v", r.Form)
		}
		if r.Form.Get("grant_type") == "refresh_token" {
			f.refreshes.Add(1)
			if r.Form.Get("refresh_token") != "refresh-1" {
				json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant"}) //nolint:errcheck
				return
			}
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"id_token": fakeIDToken("alice@example.com", time.Now().Add(time.Hour)),
			})
			return
		}
		if f.polls.Add(1) <= f.pendingPolls {
			json.NewEncoder(w).Encode(map[string]any{"error": "authorization_pending"}) //nolint:errcheck
			return
		}
		json.NewEncoder(w).Encode(f.final) //nolint:errcheck
	})
	return httptest.NewServer(mux)
}

func clientFor(ts *httptest.Server) *Client {
	return &Client{
		ClientID: "cid", ClientSecret: "csecret",
		DeviceEndpoint: ts.URL + "/device", TokenEndpoint: ts.URL + "/token",
		sleep: func(context.Context, time.Duration) error { return nil }, // no real waiting
	}
}

func TestDeviceFlow_success(t *testing.T) {
	exp := time.Now().Add(time.Hour).Truncate(time.Second)
	f := &fakeGoogle{t: t, pendingPolls: 3, final: map[string]any{
		"id_token": fakeIDToken("alice@example.com", exp), "refresh_token": "refresh-1",
	}}
	ts := f.server()
	defer ts.Close()
	c := clientFor(ts)

	dc, err := c.Start(context.Background())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if dc.UserCode != "ABCD-EFGH" || dc.VerificationURL != "https://example.test/device" {
		t.Fatalf("device code: %+v", dc)
	}
	tok, err := c.Wait(context.Background(), dc)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if tok.Email != "alice@example.com" || tok.RefreshToken != "refresh-1" || !tok.Expiry.Equal(exp) {
		t.Errorf("token: %+v", tok)
	}
	if got := f.polls.Load(); got != 4 {
		t.Errorf("polled %d times, want 4 (3 pending + 1 answer)", got)
	}
}

func TestDeviceFlow_declinedAndExpired(t *testing.T) {
	for errCode, want := range map[string]error{"access_denied": ErrDenied, "expired_token": ErrExpired} {
		f := &fakeGoogle{t: t, final: map[string]any{"error": errCode}}
		ts := f.server()
		c := clientFor(ts)
		dc, err := c.Start(context.Background())
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		if _, err := c.Wait(context.Background(), dc); !errors.Is(err, want) {
			t.Errorf("%s: got %v, want %v", errCode, err, want)
		}
		ts.Close()
	}
}

func TestDeviceFlow_noClientID(t *testing.T) {
	if _, err := (&Client{}).Start(context.Background()); err == nil {
		t.Error("Start with no client ID must fail")
	}
}

func TestStore_roundTripAndPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "auth.json")
	s := Store{Path: path}
	if _, ok, err := s.Get("https://relay.test"); ok || err != nil {
		t.Fatalf("empty store: ok=%v err=%v", ok, err)
	}
	want := Session{ClientID: "cid", Email: "a@b.c", IDToken: "x", RefreshToken: "r", Expiry: time.Now().Add(time.Hour).UTC().Truncate(time.Second)}
	if err := s.Put("https://relay.test", want); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, ok, err := s.Get("https://relay.test")
	if err != nil || !ok || got != want {
		t.Fatalf("Get: %+v ok=%v err=%v", got, ok, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("sign-in file mode = %o, want 600", info.Mode().Perm())
	}
	// Sessions for different relays do not collide.
	if _, ok, _ := s.Get("https://other.test"); ok {
		t.Error("session leaked to another relay")
	}
	if deleted, err := s.Delete("https://relay.test"); !deleted || err != nil {
		t.Fatalf("Delete: %v %v", deleted, err)
	}
	if deleted, _ := s.Delete("https://relay.test"); deleted {
		t.Error("second Delete reported a deletion")
	}
}

func TestStore_IDToken_refreshesWhenStale(t *testing.T) {
	f := &fakeGoogle{t: t}
	ts := f.server()
	defer ts.Close()
	s := Store{Path: filepath.Join(t.TempDir(), "auth.json")}
	newClient := func(Session) *Client { return clientFor(ts) }
	const relay = "https://relay.test"

	// Nobody signed in.
	if _, ok, err := s.IDToken(context.Background(), relay, newClient); ok || err != nil {
		t.Fatalf("no session: ok=%v err=%v", ok, err)
	}

	// A fresh token is returned as is, with no call to the provider.
	fresh := fakeIDToken("alice@example.com", time.Now().Add(time.Hour))
	s.Put(relay, Session{ClientID: "cid", IDToken: fresh, RefreshToken: "refresh-1", Expiry: time.Now().Add(time.Hour)}) //nolint:errcheck
	got, ok, err := s.IDToken(context.Background(), relay, newClient)
	if err != nil || !ok || got != fresh || f.refreshes.Load() != 0 {
		t.Fatalf("fresh: token changed=%v ok=%v err=%v refreshes=%d", got != fresh, ok, err, f.refreshes.Load())
	}

	// A stale one is refreshed, and the new token is saved.
	s.Put(relay, Session{ClientID: "cid", IDToken: "old", RefreshToken: "refresh-1", Expiry: time.Now().Add(30 * time.Second)}) //nolint:errcheck
	got, ok, err = s.IDToken(context.Background(), relay, newClient)
	if err != nil || !ok || got == "old" || f.refreshes.Load() != 1 {
		t.Fatalf("stale: got=%q ok=%v err=%v refreshes=%d", got, ok, err, f.refreshes.Load())
	}
	saved, _, _ := s.Get(relay)
	if saved.IDToken != got || saved.RefreshToken != "refresh-1" || saved.Email != "alice@example.com" {
		t.Errorf("refreshed session was not saved correctly: %+v", saved)
	}

	// A revoked refresh token is an error that tells the person what to do.
	s.Put(relay, Session{ClientID: "cid", IDToken: "old", RefreshToken: "revoked", Expiry: time.Now().Add(-time.Minute)}) //nolint:errcheck
	if _, ok, err := s.IDToken(context.Background(), relay, newClient); !ok || err == nil {
		t.Fatalf("revoked: ok=%v err=%v", ok, err)
	}
}
