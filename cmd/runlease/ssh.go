package main

import (
	"fmt"
	"net"
	"os"
	"time"

	"github.com/spf13/cobra"
	cryptossh "golang.org/x/crypto/ssh"
	"golang.org/x/term"

	"github.com/marshal-org/runlease/pkg/credential"
)

func init() {
	sshCmd.Flags().BoolVar(&sshDirect, "direct", false, "dial the instance's public IP directly instead of via the relay")
	rootCmd.AddCommand(sshCmd)
}

var sshDirect bool

var sshCmd = &cobra.Command{
	Use:   "ssh [name|id]",
	Short: "Open an interactive SSH session to a running instance",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runSSH,
}

func runSSH(cmd *cobra.Command, args []string) error {
	reg, err := loadRegistry()
	if err != nil {
		return err
	}
	nameOrID, err := resolveInstance(cmd, reg, args, "ssh")
	if err != nil || nameOrID == "" {
		return err
	}
	entry, err := reg.Get(nameOrID)
	if err != nil {
		return err
	}

	masterKey, err := credential.LoadOrCreateMasterKey(defaultMasterKeyPath())
	if err != nil {
		return fmt.Errorf("loading master key: %w", err)
	}
	cred, err := credential.Load(entry.CredDir, string(entry.Handle.ID), masterKey)
	if err != nil {
		return fmt.Errorf("loading credential: %w", err)
	}

	out := cmd.OutOrStdout()
	if entry.RelayAddr != "" && !sshDirect {
		fmt.Fprintf(out, "Connecting to %s via relay (%s) as runlease-agent...\n",
			entry.Handle.ManifestName, entry.RelayAddr)
		conn, err := openRelay(cmd.Context(), cmd.ErrOrStderr(), entry)
		if err != nil {
			return err
		}
		return sshOverConn(conn, entry.RelayAddr, cred)
	}
	if entry.RelayAddr != "" {
		return fmt.Errorf("instance %s was launched through a relay, so its SSH server listens on localhost only; connect with: runlease connect %s",
			entry.Handle.ID, entry.Handle.ManifestName)
	}
	if entry.Handle.PublicIP == "" {
		return fmt.Errorf("instance %s has no public IP and no relay configured", entry.Handle.ID)
	}
	fmt.Fprintf(out, "Connecting to %s (%s) as runlease-agent...\n",
		entry.Handle.ManifestName, entry.Handle.PublicIP+":22")
	return sshSession(entry.Handle.PublicIP, cred)
}

// sshSession dials publicIP:22 and opens an interactive SSH session.
// Called by runSSH and by pollAndConnect in up.go.
func sshSession(publicIP string, cred *credential.EphemeralCredential) error {
	conn, err := net.DialTimeout("tcp", publicIP+":22", 10*time.Second)
	if err != nil {
		return err
	}
	return sshOverConn(conn, publicIP+":22", cred)
}

// sshOverConn runs an interactive SSH session over an already-established
// net.Conn (used by both sshSession and the relay connect path).
func sshOverConn(conn net.Conn, addr string, cred *credential.EphemeralCredential) error {
	client, err := newSSHClient(conn, addr, cred)
	if err != nil {
		return err
	}
	defer client.Close()
	return sshInteractiveSession(client)
}

// sshInteractiveSession opens an interactive shell on the given SSH client and
// blocks until it exits. Extracted from sshOverConn so the up-with-forwards
// flow can start background forwards on the same client before calling this.
func sshInteractiveSession(client *cryptossh.Client) error {
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("opening SSH session: %w", err)
	}
	defer session.Close()

	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return fmt.Errorf("stdin is not a terminal; runlease ssh requires an interactive terminal")
	}
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("setting raw terminal: %w", err)
	}
	defer term.Restore(fd, oldState) //nolint:errcheck

	w, h, err := term.GetSize(fd)
	if err != nil {
		w, h = 80, 24
	}
	if err := session.RequestPty("xterm-256color", h, w, cryptossh.TerminalModes{
		cryptossh.ECHO:          1,
		cryptossh.TTY_OP_ISPEED: 14400,
		cryptossh.TTY_OP_OSPEED: 14400,
	}); err != nil {
		return fmt.Errorf("requesting PTY: %w", err)
	}

	session.Stdin = os.Stdin
	session.Stdout = os.Stdout
	session.Stderr = os.Stderr

	if err := session.Shell(); err != nil {
		return fmt.Errorf("starting shell: %w", err)
	}
	_ = session.Wait()
	return nil
}
