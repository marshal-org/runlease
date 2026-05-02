package relay

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/api/idtoken"
)

// AuthConfig controls who may register relay slots.
// When Audience is empty, authentication is disabled (suitable for local dev
// or a fully private relay). When Audience is set, every register call must
// supply a valid Google ID token in the Authorization header.
//
// Audience should match the relay's public URL, e.g. "https://relay.example.com".
// The CLI must request an ID token with this same audience string.
//
// Allow-list rules (evaluated after the token is verified):
//   - If both AllowedEmails and AllowedDomains are empty, any verified Google
//     identity is accepted.
//   - Otherwise, the caller's email must appear in AllowedEmails OR belong to
//     one of the AllowedDomains.
type AuthConfig struct {
	Audience       string   // expected audience; empty = auth disabled
	AllowedEmails  []string // e.g. "alice@example.com"
	AllowedDomains []string // e.g. "example.com" — matches any @example.com address
}

func (a *AuthConfig) enabled() bool { return a.Audience != "" }

// verify validates rawToken (a Google ID token) and checks the allow-list.
// Returns nil when auth is disabled.
func (a *AuthConfig) verify(ctx context.Context, rawToken string) error {
	if !a.enabled() {
		return nil
	}
	if rawToken == "" {
		return fmt.Errorf("authentication required: provide a Google ID token via Authorization: Bearer <token>")
	}
	// Try the configured audience first; fall back to gcloud's own client ID,
	// which is the audience embedded in tokens issued for personal Google accounts.
	const gcloudClientID = "32555940559.apps.googleusercontent.com"
	payload, err := idtoken.Validate(ctx, rawToken, a.Audience)
	if err != nil {
		payload, err = idtoken.Validate(ctx, rawToken, gcloudClientID)
	}
	if err != nil {
		return fmt.Errorf("invalid ID token: %w", err)
	}
	email, _ := payload.Claims["email"].(string)
	if email == "" {
		return fmt.Errorf("ID token missing email claim")
	}
	if !a.allows(email) {
		return fmt.Errorf("identity %q is not permitted on this relay", email)
	}
	return nil
}

func (a *AuthConfig) allows(email string) bool {
	if len(a.AllowedEmails) == 0 && len(a.AllowedDomains) == 0 {
		return true // audience set but no restrictions: any valid Google account
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
