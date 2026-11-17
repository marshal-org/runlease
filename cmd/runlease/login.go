package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"

	"github.com/marshal-org/runlease/pkg/config"
	"github.com/marshal-org/runlease/pkg/signin"
)

var loginRelay string

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Sign in to a relay with your Google account",
	Long: `Sign in to a relay with your Google account.

Shows a short code to approve in any browser, on any device. Nothing needs to
open on this machine, so it works over SSH and inside a container.

The relay is taken from --relay, or from 'runlease config set relay.addr'.
The session is saved in ~/.runlease/auth.json and refreshed automatically.`,
	Args: cobra.NoArgs,
	RunE: runLogin,
}

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Forget the saved sign-in for a relay",
	Args:  cobra.NoArgs,
	RunE:  runLogout,
}

var whoamiCmd = &cobra.Command{
	Use:   "whoami",
	Short: "Show who you are signed in as, your tier and how much of it is in use",
	Args:  cobra.NoArgs,
	RunE:  runWhoami,
}

func init() {
	for _, c := range []*cobra.Command{loginCmd, logoutCmd, whoamiCmd} {
		c.Flags().StringVar(&loginRelay, "relay", "", "relay address (default: relay.addr from your config)")
		rootCmd.AddCommand(c)
	}
}

func signinStore() signin.Store {
	return signin.Store{Path: filepath.Join(runleaseHome(), "auth.json")}
}

// sessionKey is how a relay is named in the sign-in file: its HTTP base URL.
func sessionKey(relayAddr string) string {
	return strings.TrimRight(relayHTTPBase(relayAddr), "/")
}

func clientForSession(s signin.Session) *signin.Client {
	return &signin.Client{ClientID: s.ClientID, ClientSecret: s.ClientSecret}
}

// relayFromFlagOrConfig resolves which relay an account command talks to.
func relayFromFlagOrConfig() (string, error) {
	if loginRelay != "" {
		return loginRelay, nil
	}
	cfg, err := config.Load(defaultConfigPath())
	if err != nil {
		return "", err
	}
	if cfg.Relay.Addr == "" {
		return "", fmt.Errorf("no relay set. For the free public relay, run:\n  runlease login --relay %s\nFor your own relay: runlease config set relay.addr wss://YOUR-RELAY", publicRelay)
	}
	return cfg.Relay.Addr, nil
}

// relayDiscovery is the relay's public description of itself.
type relayDiscovery struct {
	Version string `json:"version"`
	Auth    struct {
		Required bool `json:"required"`
		Google   *struct {
			ClientID     string `json:"clientId"`
			ClientSecret string `json:"clientSecret"`
		} `json:"google"`
	} `json:"auth"`
	DefaultTier *struct {
		Name     string `json:"name"`
		MaxSlots int    `json:"maxSlots"`
		SlotTTL  string `json:"slotTTL"`
	} `json:"defaultTier"`
}

func getJSON(ctx context.Context, url, bearer string, out any) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, errors.New(strings.TrimSpace(string(body)))
	}
	return resp.StatusCode, json.Unmarshal(body, out)
}

func fetchDiscovery(ctx context.Context, relayAddr string) (*relayDiscovery, error) {
	var d relayDiscovery
	status, err := getJSON(ctx, sessionKey(relayAddr)+"/.well-known/runlease-relay", "", &d)
	if status == http.StatusNotFound {
		return nil, errors.New("this relay does not describe itself (it predates sign-in support)")
	}
	if err != nil {
		return nil, fmt.Errorf("asking the relay how to sign in: %w", err)
	}
	return &d, nil
}

// publicRelay is the free relay run by the project. It is only ever a
// suggestion, and the one default: 'runlease login' with no relay chosen.
const publicRelay = "wss://connect.runlease.online"

func runLogin(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()
	// First run: no relay named and none saved. Signing in can then only mean
	// the public one. It is saved once sign-in has worked, not before, so a
	// cancelled login leaves later launches as they were.
	cfg, err := config.Load(defaultConfigPath())
	if err != nil {
		return err
	}
	firstRun := loginRelay == "" && cfg.Relay.Addr == ""
	relayAddr := publicRelay
	if firstRun {
		fmt.Fprintf(out, "No relay chosen yet: using the free public relay %s.\n", publicRelay)
	} else if relayAddr, err = relayFromFlagOrConfig(); err != nil {
		return err
	}

	d, err := fetchDiscovery(ctx, relayAddr)
	if err != nil {
		return err
	}
	if !d.Auth.Required {
		fmt.Fprintf(out, "%s does not require sign-in.\n", relayAddr)
		return nil
	}
	if d.Auth.Google == nil || d.Auth.Google.ClientID == "" {
		return fmt.Errorf("%s does not offer Google sign-in. It accepts an identity token from 'gcloud auth login' instead", relayAddr)
	}
	client := &signin.Client{ClientID: d.Auth.Google.ClientID, ClientSecret: d.Auth.Google.ClientSecret}

	dc, err := client.Start(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "\nTo sign in, open\n\n    %s\n\nand enter the code\n\n    %s\n\nWaiting for approval...\n", dc.VerificationURL, dc.UserCode)

	tok, err := client.Wait(ctx, dc)
	if err != nil {
		return err
	}
	sess := signin.Session{
		ClientID:     client.ClientID,
		ClientSecret: client.ClientSecret,
		Email:        tok.Email,
		IDToken:      tok.IDToken,
		RefreshToken: tok.RefreshToken,
		Expiry:       tok.Expiry,
	}
	if err := signinStore().Put(sessionKey(relayAddr), sess); err != nil {
		return err
	}
	fmt.Fprintf(out, "\n✅ Signed in as %s\n", tok.Email)
	if firstRun {
		cfg.Relay.Addr = publicRelay
		if err := config.Save(defaultConfigPath(), cfg); err != nil {
			return fmt.Errorf("signed in, but could not save the relay: %w\nRun: runlease config set relay.addr %s", err, publicRelay)
		}
		fmt.Fprintf(out, "   Saved as your relay. Change it with: runlease config set relay.addr ADDRESS\n")
	}
	// Say the tier this person actually has. The relay's default tier is only
	// the answer when the relay cannot be asked.
	if a, err := fetchAccount(cmd.Context(), relayAddr); err == nil && a.Tier != "" {
		if a.Slots.Max > 0 {
			fmt.Fprintf(out, "   %s tier: up to %d machines at once, each for up to %s.\n", a.Tier, a.Slots.Max, a.SlotTTL)
		} else {
			fmt.Fprintf(out, "   %s tier: no limit on machines, each for up to %s.\n", a.Tier, a.SlotTTL)
		}
	} else if d.DefaultTier != nil && d.DefaultTier.MaxSlots > 0 {
		fmt.Fprintf(out, "   %s tier: up to %d machines at once, each for up to %s.\n",
			d.DefaultTier.Name, d.DefaultTier.MaxSlots, d.DefaultTier.SlotTTL)
	}
	return nil
}

func runLogout(cmd *cobra.Command, _ []string) error {
	relayAddr, err := relayFromFlagOrConfig()
	if err != nil {
		return err
	}
	deleted, err := signinStore().Delete(sessionKey(relayAddr))
	if err != nil {
		return err
	}
	if deleted {
		fmt.Fprintf(cmd.OutOrStdout(), "Signed out of %s.\n", relayAddr)
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "Not signed in to %s.\n", relayAddr)
	}
	return nil
}

// relayAccount is the relay's answer to "who am I".
type relayAccount struct {
	Email string `json:"email"`
	Tier  string `json:"tier"`
	Slots struct {
		Used int `json:"used"`
		Max  int `json:"max"`
	} `json:"slots"`
	SlotTTL string `json:"slotTTL"`
	IdleTTL string `json:"idleTTL"`
}

func fetchAccount(ctx context.Context, relayAddr string) (*relayAccount, error) {
	bearer, err := relayBearer(ctx, relayAddr)
	if err != nil {
		return nil, err
	}
	var a relayAccount
	if _, err := getJSON(ctx, sessionKey(relayAddr)+"/me", bearer, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

func runWhoami(cmd *cobra.Command, _ []string) error {
	relayAddr, err := relayFromFlagOrConfig()
	if err != nil {
		return err
	}
	a, err := fetchAccount(cmd.Context(), relayAddr)
	if err != nil {
		return fmt.Errorf("%w\nRun 'runlease login' to sign in", err)
	}
	out := cmd.OutOrStdout()
	if jsonOutput() {
		return json.NewEncoder(out).Encode(a)
	}
	fmt.Fprintf(out, "%s on %s\n", a.Email, relayAddr)
	limit := "no limit"
	if a.Slots.Max > 0 {
		limit = fmt.Sprintf("%d of %d machines in use", a.Slots.Used, a.Slots.Max)
	} else if a.Slots.Used > 0 {
		limit = fmt.Sprintf("%d machines in use, no limit", a.Slots.Used)
	}
	if a.Tier != "" {
		fmt.Fprintf(out, "tier: %s (%s, up to %s each)\n", a.Tier, limit, a.SlotTTL)
	} else {
		fmt.Fprintf(out, "%s\n", limit)
	}
	return nil
}

// relayBearer returns the token to present to a relay when opening a slot.
// A saved 'runlease login' session wins. Without one it falls back to a Google
// identity token from the environment or from gcloud, which is what a private
// relay with an audience expects.
func relayBearer(ctx context.Context, relayAddr string) (string, error) {
	tok, signedIn, err := signinStore().IDToken(ctx, sessionKey(relayAddr), clientForSession)
	if err != nil {
		return "", err
	}
	if signedIn {
		return tok, nil
	}
	return googleIDToken(ctx, relayHTTPBase(relayAddr))
}

// releaseRelaySlot tells the relay a machine is gone, so its place in the
// owner's tier is free at once instead of when the slot times out.
func releaseRelaySlot(ctx context.Context, relayAddr, token string) error {
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	ws, _, err := dialer.DialContext(ctx, relayWSURL(relayAddr), nil)
	if err != nil {
		return fmt.Errorf("dial relay: %w", err)
	}
	defer ws.Close()
	msg, _ := json.Marshal(map[string]string{"role": "release", "token": token})
	ws.SetWriteDeadline(time.Now().Add(10 * time.Second)) //nolint:errcheck
	if err := ws.WriteMessage(websocket.BinaryMessage, append(msg, '\n')); err != nil {
		return fmt.Errorf("send: %w", err)
	}
	ws.SetReadDeadline(time.Now().Add(10 * time.Second)) //nolint:errcheck
	_, data, err := ws.ReadMessage()
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	var resp map[string]string
	if err := json.Unmarshal([]byte(strings.TrimRight(string(data), "\n\r")), &resp); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}
	if resp["status"] != "ok" {
		return fmt.Errorf("%s: %s", resp["status"], resp["message"])
	}
	return nil
}
