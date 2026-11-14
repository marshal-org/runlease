package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	cryptossh "golang.org/x/crypto/ssh"

	"github.com/marshal-org/runlease/pkg/credential"
	"github.com/marshal-org/runlease/pkg/manifest"
	"github.com/marshal-org/runlease/pkg/provider"
)

// setupDoneMarker is the line written to /var/log/runlease-setup.log by the
// userdata template right before /run/runlease-setup-done is touched. The
// attach tail-loop watches for it.
const setupDoneMarker = "__RUNLEASE_SETUP_DONE__"

// runAttach drives the --attach happy path after the instance is provisioned:
// wait for SSH, tail the setup log to completion, apply manifest.forwards,
// then block until Ctrl+C.
func runAttach(cmd *cobra.Command, m *manifest.Manifest, running *provider.InstanceHandle, cred *credential.EphemeralCredential, relayToken string) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	out := cmd.OutOrStdout()
	fmt.Fprintln(out, "\n⏳ Attaching: waiting for SSH tunnel...")

	conn, addr, err := waitForTunnel(ctx, m, running, relayToken)
	if err != nil {
		return err
	}
	client, err := newSSHClient(conn, addr, cred)
	if err != nil {
		return fmt.Errorf("ssh: %w", err)
	}
	defer client.Close()

	if len(m.Setup) > 0 {
		fmt.Fprintln(out, "📜 Streaming setup logs...")
		if err := tailSetup(ctx, client, out); err != nil && ctx.Err() == nil {
			fmt.Fprintf(out, "⚠️  setup tail ended: %v\n", err)
		}
	}

	specs := registryForwardsToSpecs(manifestToRegistryForwards(m.Forwards))
	if len(specs) > 0 {
		return runForwards(ctx, out, client, specs)
	}

	fmt.Fprintln(out, "\n(no forwards declared — exiting attach)")
	return nil
}

// waitForTunnel polls until we can open an SSH connection (direct or via relay)
// and returns the underlying net.Conn. Cadence mirrors pollAndConnect*.
func waitForTunnel(ctx context.Context, m *manifest.Manifest, running *provider.InstanceHandle, relayToken string) (net.Conn, string, error) {
	if m.Relay.Addr != "" {
		readyURL := relayHTTPBase(m.Relay.Addr) + "/ready"
		for {
			req, err := http.NewRequestWithContext(ctx, "GET", readyURL, nil)
			if err != nil {
				return nil, "", fmt.Errorf("building ready request: %w", err)
			}
			req.Header.Set("X-Relay-Token", relayToken)
			resp, err := http.DefaultClient.Do(req)
			if err == nil {
				ready := resp.StatusCode == http.StatusOK
				resp.Body.Close()
				if ready {
					if conn, derr := dialRelay(m.Relay.Addr, relayToken); derr == nil {
						return conn, m.Relay.Addr, nil
					}
				}
			}
			if err := sleepCtx(ctx, sshPollInterval); err != nil {
				return nil, "", err
			}
		}
	}
	if running.PublicIP == "" {
		return nil, "", fmt.Errorf("instance has no public IP and no relay configured")
	}
	for {
		d := &net.Dialer{Timeout: 5 * time.Second}
		conn, err := d.DialContext(ctx, "tcp", running.PublicIP+":22")
		if err == nil {
			return conn, running.PublicIP + ":22", nil
		}
		if err := sleepCtx(ctx, sshPollInterval); err != nil {
			return nil, "", err
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// tailSetup runs `tail -F /var/log/runlease-setup.log` over an SSH session and
// returns when the setupDoneMarker appears in the stream (or ctx is cancelled).
func tailSetup(ctx context.Context, client *cryptossh.Client, out io.Writer) error {
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()

	stdout, err := session.StdoutPipe()
	if err != nil {
		return err
	}
	session.Stderr = out

	// --retry handles the race where the log doesn't exist yet (very first second
	// of boot). -F follows by name across rotation, in case the log is rotated.
	if err := session.Start("tail --retry -n +1 -F /var/log/runlease-setup.log"); err != nil {
		return err
	}

	// markerCh is signalled the moment the markerWriter sees the marker. Buffer
	// of 1 so the writer never blocks even if we're not yet selecting on it.
	markerCh := make(chan struct{}, 1)
	copyDone := make(chan error, 1)
	go func() {
		w := &markerWriter{out: out, marker: []byte(setupDoneMarker), signal: markerCh}
		_, err := io.Copy(w, stdout)
		copyDone <- err
	}()

	defer func() {
		// Tear down the tail process so io.Copy returns and the goroutine exits.
		// SIGTERM is more universally honored by tail than SIGINT on the SSH
		// channel; we also close the session as a backup.
		_ = session.Signal(cryptossh.SIGTERM)
		_ = session.Close()
		<-copyDone // wait for the goroutine to finish so we don't leak it
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-markerCh:
		fmt.Fprintln(out, "✅ setup completed.")
		return nil
	case err := <-copyDone:
		// Stream ended (sshd closed our channel, network drop, etc.) before we
		// saw the marker. Surface a useful error rather than blocking forever.
		if err != nil {
			return fmt.Errorf("tail stream ended: %w", err)
		}
		return fmt.Errorf("tail stream ended before setup-done marker")
	}
}

// markerWriter streams bytes to `out` and signals on `signal` (non-blocking)
// the first time `marker` is observed in the stream. Writes continue to pass
// through after matching; the caller is expected to tear down the source.
type markerWriter struct {
	out     io.Writer
	marker  []byte
	signal  chan<- struct{} // optional — receives once when marker is first seen
	buf     []byte          // tail bytes carried over to handle marker split across writes
	matched bool
}

func (w *markerWriter) Write(p []byte) (int, error) {
	n, err := w.out.Write(p)
	if w.matched {
		return n, err
	}
	keep := len(w.marker) - 1

	// Fast path: marker fully contained in this write — no allocation. p can be
	// up to io.Copy's 32KB buffer, so avoid concatenating buf||p every write.
	matched := bytes.Contains(p, w.marker)
	if !matched && len(w.buf) > 0 {
		// A split marker can only straddle the carried tail and the first `keep`
		// bytes of p, so only check that small boundary.
		boundary := p
		if len(boundary) > keep {
			boundary = boundary[:keep]
		}
		matched = bytes.Contains(append(w.buf, boundary...), w.marker)
	}
	if matched {
		w.matched = true
		w.buf = nil
		if w.signal != nil {
			// Non-blocking send: buffer of 1 plus default case so a slow
			// receiver never wedges the writer.
			select {
			case w.signal <- struct{}{}:
			default:
			}
		}
		return n, err
	}

	// Carry the last `keep` bytes forward to catch a marker split across writes.
	if len(p) >= keep {
		w.buf = append(w.buf[:0], p[len(p)-keep:]...)
	} else {
		w.buf = append(w.buf, p...)
		if len(w.buf) > keep {
			w.buf = append(w.buf[:0], w.buf[len(w.buf)-keep:]...)
		}
	}
	return n, err
}
