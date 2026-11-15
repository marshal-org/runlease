package relay

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/api/idtoken"
)

// Identity is who opened a slot. It is empty when the relay runs without
// authentication.
type Identity struct {
	Email string // lower-cased, verified
}

// Verifier turns a bearer token into an Identity. The default verifier accepts
// Google ID tokens; tests and other identity providers can supply their own.
type Verifier func(ctx context.Context, rawToken string) (Identity, error)

// gcloudClientID is the audience of tokens printed by
// `gcloud auth print-identity-token` for a personal Google account, which
// cannot request a custom audience.
const gcloudClientID = "32555940559.apps.googleusercontent.com"

// AuthConfig controls who may register relay slots.
//
// Authentication is on when any of Audience, GoogleClientIDs or Verifier is
// set. With it on, every register call must carry a bearer token.
//
// Accepted Google ID token audiences:
//   - Audience: the relay's public URL, e.g. "https://relay.example.com". Used
//     by service accounts and `gcloud auth print-identity-token --audiences`.
//     Setting it also accepts gcloud's own client ID, for personal accounts.
//   - GoogleClientIDs: OAuth client IDs. `runlease login` signs the user in
//     with one of these and presents the resulting ID token.
//
// Allow-list rules (evaluated after the token is verified):
//   - If both AllowedEmails and AllowedDomains are empty, any verified Google
//     identity is accepted. That is the setting for a public relay; limits
//     then come from tiers.
//   - Otherwise, the caller's email must appear in AllowedEmails OR belong to
//     one of the AllowedDomains.
type AuthConfig struct {
	Audience        string
	GoogleClientIDs []string
	AllowedEmails   []string // e.g. "alice@example.com"
	AllowedDomains  []string // e.g. "example.com" — matches any @example.com address
	Verifier        Verifier // optional; replaces Google ID token validation
}

func (a *AuthConfig) enabled() bool {
	return a.Audience != "" || len(a.GoogleClientIDs) > 0 || a.Verifier != nil
}

// audiences lists every ID token audience this relay accepts.
func (a *AuthConfig) audiences() []string {
	var out []string
	if a.Audience != "" {
		out = append(out, a.Audience)
	}
	out = append(out, a.GoogleClientIDs...)
	if a.Audience != "" {
		out = append(out, gcloudClientID)
	}
	return out
}

// identify validates rawToken and checks the allow-list. With authentication
// off it returns the empty Identity and no error.
func (a *AuthConfig) identify(ctx context.Context, rawToken string) (Identity, error) {
	if !a.enabled() {
		return Identity{}, nil
	}
	if rawToken == "" {
		return Identity{}, fmt.Errorf("authentication required: run 'runlease login', or provide a Google ID token via Authorization: Bearer <token>")
	}
	verify := a.Verifier
	if verify == nil {
		verify = a.verifyGoogle
	}
	id, err := verify(ctx, rawToken)
	if err != nil {
		return Identity{}, err
	}
	id.Email = strings.ToLower(strings.TrimSpace(id.Email))
	if id.Email == "" {
		return Identity{}, fmt.Errorf("token carries no email")
	}
	if !a.allows(id.Email) {
		return Identity{}, fmt.Errorf("identity %q is not permitted on this relay", id.Email)
	}
	return id, nil
}

// verifyGoogle validates a Google ID token against each accepted audience.
func (a *AuthConfig) verifyGoogle(ctx context.Context, rawToken string) (Identity, error) {
	var payload *idtoken.Payload
	var err error
	for _, aud := range a.audiences() {
		payload, err = idtoken.Validate(ctx, rawToken, aud)
		if err == nil {
			break
		}
	}
	if err != nil || payload == nil {
		return Identity{}, fmt.Errorf("invalid ID token: %w", err)
	}
	email, _ := payload.Claims["email"].(string)
	if email == "" {
		return Identity{}, fmt.Errorf("ID token missing email claim")
	}
	// An unverified address proves nothing about who holds the account, so it
	// must not be matched against the allow-list or counted against a tier.
	if verified, ok := payload.Claims["email_verified"].(bool); !ok || !verified {
		return Identity{}, fmt.Errorf("ID token email %q is not verified", email)
	}
	return Identity{Email: email}, nil
}

func (a *AuthConfig) allows(email string) bool {
	if len(a.AllowedEmails) == 0 && len(a.AllowedDomains) == 0 {
		return true // no restrictions: any verified identity
	}
	email = strings.ToLower(email)
	for _, e := range a.AllowedEmails {
		if strings.ToLower(e) == email {
			return true
		}
	}
	if idx := strings.LastIndex(email, "@"); idx >= 0 {
		domain := email[idx+1:]
		for _, d := range a.AllowedDomains {
			if strings.ToLower(d) == domain {
				return true
			}
		}
	}
	return false
}
