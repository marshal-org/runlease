package main

import (
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	cryptossh "golang.org/x/crypto/ssh"
	"golang.org/x/term"

	"github.com/runlease/runlease/pkg/credential"
)

func init() {
	rootCmd.AddCommand(sshCmd)
}

var sshCmd = &cobra.Command{
	Use:   "ssh <name|id>",
	Short: "Open an interactive SSH session to a running instance",
	Args:  cobra.ExactArgs(1),
	RunE:  runSSH,
}

func runSSH(cmd *cobra.Command, args []string) error {
	reg, err := loadRegistry()
	if err != nil {
		return err
	}
	entry, err := reg.Get(args[0])
	if err != nil {
		return err
	}
	if entry.Handle.PublicIP == "" {
		return fmt.Errorf("instance %s has no public IP — is it running?", entry.Handle.ID)
	}

	masterKey, err := credential.LoadOrCreateMasterKey(defaultMasterKeyPath())
	if err != nil {
		return fmt.Errorf("loading master key: %w", err)
	}
	cred, err := credential.Load(entry.CredDir, string(entry.Handle.ID), masterKey)
	if err != nil {
		return fmt.Errorf("loading credential: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Connecting to %s (%s) as runlease-agent...\n",
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
	signer, err := cryptossh.ParsePrivateKey(cred.PrivateKeyPEM)
	if err != nil {
		return fmt.Errorf("parsing agent private key: %w", err)
	}

	hostPub, _, _, _, err := cryptossh.ParseAuthorizedKey(
		[]byte(strings.TrimSuffix(cred.HostPublicKeySSH, "\n")),
	)
	if err != nil {
		return fmt.Errorf("parsing host public key: %w", err)
	}

	config := &cryptossh.ClientConfig{
		User:              "runlease-agent",
		Auth:              []cryptossh.AuthMethod{cryptossh.PublicKeys(signer)},
		HostKeyCallback:   cryptossh.FixedHostKey(hostPub),
		HostKeyAlgorithms: []string{"ssh-ed25519"},
	}

	// Apply handshake timeout via deadline; NewClientConn doesn't use Timeout.
	conn.SetDeadline(time.Now().Add(10 * time.Second)) //nolint:errcheck
	c, chans, reqs, err := cryptossh.NewClientConn(conn, addr, config)
	if err != nil {
		return err
	}
	conn.SetDeadline(time.Time{}) //nolint:errcheck

	client := cryptossh.NewClient(c, chans, reqs)
	defer client.Close()

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
