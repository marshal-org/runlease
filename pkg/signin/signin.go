// Package signin signs a person in to a relay with their Google account and
// keeps the resulting session on disk.
//
// It uses the OAuth 2.0 device authorization grant: the CLI shows a short
// code, the person approves it in any browser on any device, and the CLI
// receives an ID token. That works over SSH, inside a container, and for an
// agent whose human is at another screen, where a browser redirect to
// localhost would not.
package signin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Google's OAuth endpoints for the device grant.
const (
	GoogleDeviceEndpoint = "https://oauth2.googleapis.com/device/code"
	GoogleTokenEndpoint  = "https://oauth2.googleapis.com/token"
)

// ErrDenied is returned when the person refuses the request in the browser.
var ErrDenied = errors.New("sign-in was declined")

// ErrExpired is returned when the code was not approved in time.
var ErrExpired = errors.New("sign-in code expired before it was approved")

// Client runs the device grant against one OAuth client.
type Client struct {
	ClientID       string
	ClientSecret   string
	DeviceEndpoint string       // default GoogleDeviceEndpoint
	TokenEndpoint  string       // default GoogleTokenEndpoint
	HTTP           *http.Client // default: 30s timeout
	// sleep is replaced in tests.
	sleep func(context.Context, time.Duration) error
}

// DeviceCode is what the person needs to approve the sign-in.
type DeviceCode struct {
	DeviceCode      string
	UserCode        string
	VerificationURL string
	Interval        time.Duration
	ExpiresAt       time.Time
}

// Token is the result of a sign-in or a refresh.
type Token struct {
	IDToken      string
	RefreshToken string // empty on a refresh that did not rotate it
	Email        string
	Expiry       time.Time
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (c *Client) post(ctx context.Context, endpoint string, form url.Values, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http().Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return resp.StatusCode, fmt.Errorf("unexpected response (HTTP %d)", resp.StatusCode)
	}
	return resp.StatusCode, nil
}

// Start asks for a device code.
func (c *Client) Start(ctx context.Context) (*DeviceCode, error) {
	if c.ClientID == "" {
		return nil, errors.New("this relay does not offer Google sign-in")
	}
	endpoint := c.DeviceEndpoint
	if endpoint == "" {
		endpoint = GoogleDeviceEndpoint
	}
	var r struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURL string `json:"verification_url"`
		VerificationURI string `json:"verification_uri"`
		ExpiresIn       int    `json:"expires_in"`
		Interval        int    `json:"interval"`
		Error           string `json:"error"`
		ErrorDesc       string `json:"error_description"`
	}
	form := url.Values{"client_id": {c.ClientID}, "scope": {"openid email"}}
	if _, err := c.post(ctx, endpoint, form, &r); err != nil {
		return nil, fmt.Errorf("starting sign-in: %w", err)
	}
	if r.Error != "" || r.DeviceCode == "" {
		return nil, fmt.Errorf("starting sign-in: %s %s", r.Error, r.ErrorDesc)
	}
	if r.VerificationURL == "" {
		r.VerificationURL = r.VerificationURI
	}
	if r.Interval <= 0 {
		r.Interval = 5
	}
	return &DeviceCode{
		DeviceCode:      r.DeviceCode,
		UserCode:        r.UserCode,
		VerificationURL: r.VerificationURL,
		Interval:        time.Duration(r.Interval) * time.Second,
		ExpiresAt:       time.Now().Add(time.Duration(r.ExpiresIn) * time.Second),
	}, nil
}

type tokenResponse struct {
	IDToken      string `json:"id_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

func (c *Client) token(ctx context.Context, form url.Values) (*tokenResponse, error) {
	endpoint := c.TokenEndpoint
	if endpoint == "" {
		endpoint = GoogleTokenEndpoint
	}
	form.Set("client_id", c.ClientID)
	form.Set("client_secret", c.ClientSecret)
	var r tokenResponse
	if _, err := c.post(ctx, endpoint, form, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// Wait polls until the person approves or declines the code, or it expires.
func (c *Client) Wait(ctx context.Context, dc *DeviceCode) (*Token, error) {
	sleep := c.sleep
	if sleep == nil {
		sleep = sleepCtx
	}
	interval := dc.Interval
	for {
		if err := sleep(ctx, interval); err != nil {
			return nil, err
		}
		if time.Now().After(dc.ExpiresAt) {
			return nil, ErrExpired
		}
		r, err := c.token(ctx, url.Values{
			"device_code": {dc.DeviceCode},
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		})
		if err != nil {
			return nil, fmt.Errorf("waiting for approval: %w", err)
		}
		switch r.Error {
		case "":
			return tokenFrom(r)
		case "authorization_pending":
		case "slow_down":
			interval += 5 * time.Second
		case "access_denied":
			return nil, ErrDenied
		case "expired_token":
			return nil, ErrExpired
		default:
			return nil, fmt.Errorf("sign-in failed: %s %s", r.Error, r.ErrorDesc)
		}
	}
}

// Refresh trades a refresh token for a fresh ID token.
func (c *Client) Refresh(ctx context.Context, refreshToken string) (*Token, error) {
	r, err := c.token(ctx, url.Values{
		"refresh_token": {refreshToken},
		"grant_type":    {"refresh_token"},
	})
	if err != nil {
		return nil, fmt.Errorf("refreshing sign-in: %w", err)
	}
	if r.Error != "" {
		return nil, fmt.Errorf("refreshing sign-in: %s %s", r.Error, r.ErrorDesc)
	}
	return tokenFrom(r)
}

func tokenFrom(r *tokenResponse) (*Token, error) {
	if r.IDToken == "" {
		return nil, errors.New("the identity provider returned no ID token")
	}
	email, exp, err := peek(r.IDToken)
	if err != nil {
		return nil, err
	}
	return &Token{IDToken: r.IDToken, RefreshToken: r.RefreshToken, Email: email, Expiry: exp}, nil
}

// peek reads the email and expiry out of an ID token WITHOUT verifying it.
// That is enough to label a session and to know when to refresh. The relay is
// the party that verifies the signature; nothing here is a security decision.
func peek(idToken string) (string, time.Time, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return "", time.Time{}, errors.New("malformed ID token")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", time.Time{}, errors.New("malformed ID token")
	}
	var claims struct {
		Email string `json:"email"`
		Exp   int64  `json:"exp"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil {
		return "", time.Time{}, errors.New("malformed ID token")
	}
	return claims.Email, time.Unix(claims.Exp, 0), nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// ── stored sessions ───────────────────────────────────────────────────────────

// Session is one relay's saved sign-in.
type Session struct {
	ClientID     string    `json:"clientId"`
	ClientSecret string    `json:"clientSecret,omitempty"`
	Email        string    `json:"email"`
	IDToken      string    `json:"idToken"`
	RefreshToken string    `json:"refreshToken"`
	Expiry       time.Time `json:"expiry"`
}

// Store keeps sessions in one file, keyed by relay URL. The file holds a
// refresh token, so it is written with mode 0600 like the rest of ~/.runlease.
type Store struct{ Path string }

func (s Store) load() (map[string]Session, error) {
	raw, err := os.ReadFile(s.Path)
	if os.IsNotExist(err) {
		return map[string]Session{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading sign-in file: %w", err)
	}
	out := map[string]Session{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parsing sign-in file %s: %w", s.Path, err)
	}
	return out, nil
}

func (s Store) save(all map[string]Session) error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	// A temp file of its own (created 0600), so two CLIs refreshing at the same
	// time cannot write through, or rename away, each other's file.
	tmp, err := os.CreateTemp(filepath.Dir(s.Path), filepath.Base(s.Path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("writing sign-in file: %w", err)
	}
	_, err = tmp.Write(raw)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), s.Path)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("writing sign-in file: %w", err)
	}
	return nil
}

// Get returns the session for a relay, if there is one.
func (s Store) Get(relay string) (Session, bool, error) {
	all, err := s.load()
	if err != nil {
		return Session{}, false, err
	}
	sess, ok := all[relay]
	return sess, ok, nil
}

// Put saves the session for a relay.
func (s Store) Put(relay string, sess Session) error {
	all, err := s.load()
	if err != nil {
		return err
	}
	all[relay] = sess
	return s.save(all)
}

// Delete removes the session for a relay. It reports whether one existed.
func (s Store) Delete(relay string) (bool, error) {
	all, err := s.load()
	if err != nil {
		return false, err
	}
	if _, ok := all[relay]; !ok {
		return false, nil
	}
	delete(all, relay)
	return true, s.save(all)
}

// IDToken returns a currently valid ID token for a relay, refreshing and
// saving the session when the stored token is about to expire. The bool is
// false when nobody is signed in to that relay.
func (s Store) IDToken(ctx context.Context, relay string, newClient func(Session) *Client) (string, bool, error) {
	sess, ok, err := s.Get(relay)
	if err != nil || !ok {
		return "", false, err
	}
	if time.Until(sess.Expiry) > 2*time.Minute {
		return sess.IDToken, true, nil
	}
	if sess.RefreshToken == "" {
		return "", true, errors.New("sign-in expired: run 'runlease login' again")
	}
	tok, err := newClient(sess).Refresh(ctx, sess.RefreshToken)
	if err != nil {
		return "", true, fmt.Errorf("%w: run 'runlease login' again", err)
	}
	sess.IDToken, sess.Expiry = tok.IDToken, tok.Expiry
	if tok.RefreshToken != "" {
		sess.RefreshToken = tok.RefreshToken
	}
	if tok.Email != "" {
		sess.Email = tok.Email
	}
	if err := s.Put(relay, sess); err != nil {
		return "", true, err
	}
	return sess.IDToken, true, nil
}
