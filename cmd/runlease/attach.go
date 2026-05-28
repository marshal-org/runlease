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

	"github.com/runlease/runlease/pkg/credential"
	"github.com/runlease/runlease/pkg/manifest"
	"github.com/runlease/runlease/pkg/provider"
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
			req, _ := http.NewRequestWithContext(ctx, "GET", readyURL, nil)
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

	doneCh := make(chan error, 1)
	go func() {
		w := &markerWriter{out: out, marker: []byte(setupDoneMarker)}
		_, _ = io.Copy(w, stdout)
		if w.matched {
			doneCh <- nil
			return
		}
		doneCh <- fmt.Errorf("tail stream ended before setup-done marker")
	}()

	select {
	case <-ctx.Done():
		_ = session.Signal(cryptossh.SIGINT)
		_ = session.Close()
		return ctx.Err()
	case err := <-doneCh:
		_ = session.Signal(cryptossh.SIGINT)
		fmt.Fprintln(out, "✅ setup completed.")
		return err
	}
}

// markerWriter streams bytes to `out` and flips `matched` true once `marker`
// is observed in the stream. After matching, further writes still pass through
// but the caller is expected to close the underlying source.
type markerWriter struct {
	out     io.Writer
	marker  []byte
	buf     []byte // tail bytes carried over to handle marker split across writes
	matched bool
}

func (w *markerWriter) Write(p []byte) (int, error) {
	n, err := w.out.Write(p)
	if w.matched {
		return n, err
	}
	// Look for the marker in (buf || p), keep a tail of len(marker)-1 bytes.
	combined := append(w.buf, p...)
	if bytes.Contains(combined, w.marker) {
		w.matched = true
		w.buf = nil
		return n, err
	}
	keep := len(w.marker) - 1
	if len(combined) > keep {
		w.buf = append(w.buf[:0], combined[len(combined)-keep:]...)
	} else {
		w.buf = combined
	}
	return n, err
}
