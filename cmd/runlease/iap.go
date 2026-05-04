package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"time"

	"github.com/spf13/cobra"

	"github.com/runlease/runlease/pkg/credential"
)

func init() {
	rootCmd.AddCommand(iapCmd)
}

var iapCmd = &cobra.Command{
	Use:   "iap [name|id]",
	Short: "Open an SSH session to a GCP instance via IAP TCP tunnel",
	Long: `Open an interactive SSH session via GCP Identity-Aware Proxy TCP tunneling.

Requires:
  - gcloud CLI installed and authenticated (gcloud auth login)
  - The instance was launched on GCP (provider: gcp)
  - Your account has the roles/iap.tunnelResourceAccessor IAM role
  - The instance does NOT need a public IP or open firewall rules`,
	Args: cobra.MaximumNArgs(1),
	RunE: runIAPSession,
}

func runIAPSession(cmd *cobra.Command, args []string) error {
	reg, err := loadRegistry()
	if err != nil {
		return err
	}
	nameOrID, err := resolveInstance(cmd, reg, args, "iap")
	if err != nil || nameOrID == "" {
		return err
	}
	entry, err := reg.Get(nameOrID)
	if err != nil {
		return err
	}
	if entry.Handle.Provider != "gcp" {
		return fmt.Errorf("iap only works with GCP instances (this instance is %s)", entry.Handle.Provider)
	}

	if _, err := exec.LookPath("gcloud"); err != nil {
		return fmt.Errorf("gcloud CLI not found in PATH — install it to use IAP tunnels")
	}

	masterKey, err := credential.LoadOrCreateMasterKey(defaultMasterKeyPath())
	if err != nil {
		return fmt.Errorf("loading master key: %w", err)
	}
	cred, err := credential.Load(entry.CredDir, string(entry.Handle.ID), masterKey)
	if err != nil {
		return fmt.Errorf("loading credential: %w", err)
	}

	localPort, err := freePort()
	if err != nil {
		return fmt.Errorf("finding free local port: %w", err)
	}

	instanceName := string(entry.Handle.ID)
	zone := entry.Handle.Region

	fmt.Fprintf(cmd.OutOrStdout(), "Starting IAP tunnel to %s (%s) on localhost:%d...\n",
		entry.Handle.ManifestName, instanceName, localPort)

	tunnel := exec.CommandContext(cmd.Context(), "gcloud", "compute", "start-iap-tunnel",
		instanceName,
		"22",
		fmt.Sprintf("--zone=%s", zone),
		fmt.Sprintf("--local-host-port=localhost:%d", localPort),
	)
	tunnel.Stderr = os.Stderr

	if err := tunnel.Start(); err != nil {
		return fmt.Errorf("starting IAP tunnel: %w", err)
	}
	defer tunnel.Process.Kill() //nolint:errcheck

	addr := fmt.Sprintf("localhost:%d", localPort)
	if err := waitForTCP(cmd.Context(), addr, 60*time.Second); err != nil {
		return fmt.Errorf("IAP tunnel not ready: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Tunnel ready. Connecting via SSH...\n")

	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return fmt.Errorf("dialing tunnel: %w", err)
	}
	return sshOverConn(conn, addr, cred)
}

// freePort asks the OS for an available local TCP port.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port, nil
}

// waitForTCP polls addr until it accepts a connection or the deadline passes.
func waitForTCP(ctx context.Context, addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 1*time.Second)
		if err == nil {
			conn.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("cancelled")
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("timed out after %s", timeout)
}
