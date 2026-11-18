package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/marshal-org/runlease/pkg/provider"
	"github.com/marshal-org/runlease/pkg/registry"
)

// defaultAgentWait bounds how long a command waits for a machine's agent to
// reach the relay when no --timeout applies.
const defaultAgentWait = 5 * time.Minute

// errSlotGone means the relay does not know the machine's slot: it restarted,
// or the slot passed its lifetime. Waiting longer cannot help.
var errSlotGone = errors.New("the relay no longer has a slot for this machine (the relay restarted, or the slot expired)")

// agentWait describes one wait for a machine's agent to connect to the relay.
type agentWait struct {
	relayAddr, token string
	name             string        // for messages
	timeout          time.Duration // 0 means defaultAgentWait
	// bootFailure, if set, is asked now and then whether the machine's boot
	// script reported a failure. It returns "" when it did not, or cannot tell.
	bootFailure func(ctx context.Context) string
}

// waitForAgent polls the relay until the machine's agent is connected. It
// returns nil when it is, ctx.Err() if the user stops it, and otherwise an
// error that says why waiting on is pointless: the slot is gone, the boot
// script failed, or the time is up.
func waitForAgent(ctx context.Context, out io.Writer, w agentWait) error {
	timeout := w.timeout
	if timeout <= 0 {
		timeout = defaultAgentWait
	}
	deadline := time.Now().Add(timeout)
	readyURL := relayHTTPBase(w.relayAddr) + "/ready"

	for poll := 1; ; poll++ {
		req, err := http.NewRequestWithContext(ctx, "GET", readyURL, nil)
		if err != nil {
			return fmt.Errorf("building ready request: %w", err)
		}
		req.Header.Set("X-Relay-Token", w.token)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			status := resp.StatusCode
			resp.Body.Close()
			switch status {
			case http.StatusOK:
				return nil
			case http.StatusNotFound:
				return errSlotGone
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		// The console is a cloud API call, so not on every poll.
		if w.bootFailure != nil && poll%2 == 0 {
			if why := w.bootFailure(ctx); why != "" {
				return fmt.Errorf("the machine's boot script failed: %s\n"+
					"It will not connect. Remove it with: runlease down %s", why, w.name)
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the machine's agent did not reach the relay within %s.\n"+
				"Usual causes: the machine has no route to the relay (no NAT gateway and no public IP), or its boot script failed.\n"+
				"The machine is still running. Try 'runlease connect %s' later, or remove it with 'runlease down %s'",
				timeout, w.name, w.name)
		}
		fmt.Fprintf(out, "⏳ Agent not ready yet, retrying in %s...\n", sshPollInterval)
		if err := sleepCtx(ctx, sshPollInterval); err != nil {
			return err
		}
	}
}

var bootFailedLine = regexp.MustCompile(`RUNLEASE_BOOT_FAILED stage=(\S+) exit=(\d+)`)

// bootFailureProbe returns a probe for waitForAgent that reads the machine's
// console for the boot script's failure line. It returns nil if the provider
// cannot read a console.
func bootFailureProbe(p provider.CloudProvider, id provider.InstanceID) func(context.Context) string {
	reader, ok := p.(provider.ConsoleReader)
	if !ok {
		return nil
	}
	return func(ctx context.Context) string {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		console, err := reader.ConsoleOutput(ctx, id)
		if err != nil {
			return ""
		}
		return explainBootFailure(console)
	}
}

// explainBootFailure turns the boot script's failure line into a sentence, or
// returns "" if the console has none.
func explainBootFailure(console string) string {
	m := bootFailedLine.FindStringSubmatch(console)
	if m == nil {
		return ""
	}
	why := map[string]string{
		"lease":          "it could not arm the lease timer",
		"user":           "it could not create the login user",
		"sshd":           "the SSH lock-down could not be confirmed, so SSH was stopped",
		"agent-download": "it could not download the agent from the relay (no route out, or the relay is unreachable from the machine)",
		"agent-verify":   "the agent served by the relay does not match the checksum this CLI expects (the relay is older than the release that serves agents by digest, or it served another binary)",
		"agent-start":    "the agent could not be started",
		"setup":          "a setup step from the manifest failed",
	}[m[1]]
	if why == "" {
		why = "stage " + m[1]
	}
	return fmt.Sprintf("%s (exit %s)", why, m[2])
}

// registerSlot is registerRelayToken, as a variable so a test can register
// with an in-process relay without looking for Google credentials.
var registerSlot = registerRelayToken

// openRelay connects to a machine through its relay. A relay that restarted,
// or a slot past its lifetime, answers "unauthorized": the token is then
// registered again and the dial repeated once, so every command recovers the
// way 'connect' does. The agent redials every few seconds, and the relay holds
// the client until it arrives.
func openRelay(ctx context.Context, errOut io.Writer, e *registry.Entry) (net.Conn, error) {
	conn, err := dialRelay(e.RelayAddr, e.RelayToken)
	if err == nil {
		return conn, nil
	}
	if !strings.Contains(err.Error(), "unauthorized") {
		return nil, fmt.Errorf("relay: %w", err)
	}
	fmt.Fprintln(errOut, "The relay has no slot for this machine (it restarted, or the slot expired). Registering it again...")
	if rerr := registerSlot(ctx, e.RelayAddr, e.RelayToken); rerr != nil {
		return nil, fmt.Errorf("relay: registering the slot again: %w", rerr)
	}
	conn, err = dialRelay(e.RelayAddr, e.RelayToken)
	if err != nil {
		return nil, fmt.Errorf("relay: the slot is registered again, but the machine's agent has not reconnected yet (%w). Try again in a few seconds", err)
	}
	return conn, nil
}

// releaseTag matches a release version such as v0.9.0. A source build ("dev")
// has no release to fetch an agent from.
var releaseTag = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

// agentByDigestURL is where a machine downloads the agent with this digest. The
// version lets a relay fetch the agent of a release it does not hold yet.
func agentByDigestURL(relayAddr, digest string) string {
	u := relayHTTPBase(relayAddr) + "/agent/sha256/" + digest
	if releaseTag.MatchString(version) {
		u += "?version=" + version
	}
	return u
}

// agentProbeError means the relay could not be asked. The launch may go on: a
// machine checks the digest itself at boot.
type agentProbeError struct{ err error }

func (e *agentProbeError) Error() string { return e.err.Error() }
func (e *agentProbeError) Unwrap() error { return e.err }

// agentMismatchError means the relay cannot serve the agent this CLI expects:
// a machine launched now would refuse what it gets and never connect.
type agentMismatchError struct{ relayAddr, want, got string }

func (e *agentMismatchError) Error() string {
	return fmt.Sprintf("the relay at %s cannot serve the agent this CLI was built with, so a machine launched now would refuse what it gets and never connect.\n"+
		"  expected SHA-256: %s\n  relay serves:     %s\n"+
		"The relay is older than the release that serves agents by digest, or it cannot reach the GitHub release of this CLI. "+
		"Upgrade the relay, use the CLI release that matches it, or set relay.agentSha256 in the manifest to the digest of the agent you trust",
		e.relayAddr, e.want, e.got)
}

// resolveRelayAgent returns the URL a machine should download its agent from.
// It asks for the agent by digest first, so a relay of any release can serve
// this CLI's agent. A relay older than that route serves only its own agent at
// /agent, which is used when it happens to be the right one.
func resolveRelayAgent(ctx context.Context, relayAddr, want string) (string, error) {
	legacy := relayHTTPBase(relayAddr) + "/agent"
	if want == "" {
		return legacy, nil
	}
	byDigest := agentByDigestURL(relayAddr, want)
	got, status, err := downloadDigest(ctx, byDigest)
	switch {
	case err != nil:
		return byDigest, &agentProbeError{err}
	case status == http.StatusOK && got == want:
		return byDigest, nil
	case status == http.StatusOK:
		// The relay answered for this digest with other bytes. The boot
		// script would refuse them, so say it now.
		return "", &agentMismatchError{relayAddr, want, got}
	}
	got, status, err = downloadDigest(ctx, legacy)
	switch {
	case err != nil:
		return "", &agentProbeError{err}
	case status != http.StatusOK:
		return "", &agentProbeError{fmt.Errorf("GET /agent: %d", status)}
	case got == want:
		return legacy, nil
	}
	return "", &agentMismatchError{relayAddr, want, got}
}

// checkRelayAgent picks the agent URL for the boot script and stops a launch
// whose machine would refuse its agent and sit unreachable and billed until
// its lease ran out. If the relay cannot be asked, the launch goes on: the
// machine makes the same check itself.
func checkRelayAgent(ctx context.Context, errOut io.Writer, relayAddr, want string) (string, error) {
	if relayAddr == "" {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	u, err := resolveRelayAgent(ctx, relayAddr, want)
	var probe *agentProbeError
	if errors.As(err, &probe) {
		fmt.Fprintf(errOut, "note: could not check the relay's agent binary before launch (%v)\n", err)
		return agentByDigestURL(relayAddr, want), nil
	}
	return u, err
}

// downloadDigest GETs url and returns the SHA-256 of a 200 body, or the status.
func downloadDigest(ctx context.Context, url string) (string, int, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", 0, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", resp.StatusCode, nil
	}
	h := sha256.New()
	if _, err := io.Copy(h, resp.Body); err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), resp.StatusCode, nil
}
