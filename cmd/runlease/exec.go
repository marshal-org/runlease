package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	cryptossh "golang.org/x/crypto/ssh"

	"github.com/runlease/runlease/pkg/credential"
)

func init() {
	execCmd.Flags().BoolVarP(&execViaRelay, "relay", "R", false, "force relay even when a public IP is available")
	execCmd.Flags().BoolVar(&execNoStdin, "no-stdin", false, "do not forward local stdin to the remote command")
	rootCmd.AddCommand(execCmd)
}

var (
	execViaRelay bool
	execNoStdin  bool
)

var execCmd = &cobra.Command{
	Use:   "exec [name|id] -- <command...>",
	Short: "Run a command on the instance non-interactively",
	Long: `Run a command on the instance over the existing SSH/relay tunnel without
allocating a PTY. Stdout/stderr stream back; exit code is propagated.

Prefers the relay when the instance has one configured. Use -R to force relay
when a public IP is also present.

Examples:
  runlease exec my-runner -- uname -a
  runlease exec my-runner -- bash -c 'curl -sfL https://get.k3s.io | sudo sh -'
  echo "FOO=bar" | runlease exec my-runner -- tee /tmp/env`,
	DisableFlagParsing: false,
	Args:               cobra.MinimumNArgs(1),
	RunE:               runExec,
}

func runExec(cmd *cobra.Command, args []string) error {
	// Split args at "--" so the instance name is optional.
	// Cobra strips the "--" itself, so anything before the first non-flag arg
	// is the instance name. Convention: first arg is instance, rest is command.
	if len(args) < 1 {
		return fmt.Errorf("usage: runlease exec [name] -- <command...>")
	}

	reg, err := loadRegistry()
	if err != nil {
		return err
	}

	var nameOrID string
	var remoteArgs []string
	// Try first arg as a registered instance; if it is, the rest is the command.
	if _, err := reg.Get(args[0]); err == nil {
		nameOrID = args[0]
		remoteArgs = args[1:]
	} else {
		// First arg isn't a known instance — auto-resolve if exactly one is running.
		resolved, rerr := resolveInstance(cmd, reg, nil, "exec")
		if rerr != nil || resolved == "" {
			return rerr
		}
		nameOrID = resolved
		remoteArgs = args
	}
	if len(remoteArgs) == 0 {
		return fmt.Errorf("no command given — usage: runlease exec [name] -- <command...>")
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

	useRelay := execViaRelay || entry.Handle.PublicIP == ""
	if entry.RelayAddr != "" && useRelay {
		conn, err := dialRelay(entry.RelayAddr, entry.RelayToken)
		if err != nil {
			return fmt.Errorf("relay: %w", err)
		}
		return execOverConn(conn, entry.RelayAddr, cred, remoteArgs)
	}
	if entry.Handle.PublicIP == "" {
		return fmt.Errorf("instance %s has no public IP and no relay configured", entry.Handle.ID)
	}
	conn, err := net.DialTimeout("tcp", entry.Handle.PublicIP+":22", 10*time.Second)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	return execOverConn(conn, entry.Handle.PublicIP+":22", cred, remoteArgs)
}

// execOverConn runs a single command over an SSH client built on conn,
// without requesting a PTY. Exit code is propagated to os.Exit on error.
func execOverConn(conn net.Conn, addr string, cred *credential.EphemeralCredential, remoteArgs []string) error {
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

	conn.SetDeadline(time.Now().Add(10 * time.Second)) //nolint:errcheck
	c, chans, reqs, err := cryptossh.NewClientConn(conn, addr, config)
	if err != nil {
		conn.Close()
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

	session.Stdout = os.Stdout
	session.Stderr = os.Stderr
	if !execNoStdin {
		session.Stdin = os.Stdin
	}

	cmdStr := quoteRemoteCommand(remoteArgs)
	if err := session.Start(cmdStr); err != nil {
		return fmt.Errorf("starting remote command: %w", err)
	}
	err = session.Wait()
	exit := 0
	switch e := err.(type) {
	case nil:
		// success
	case *cryptossh.ExitError:
		exit = e.ExitStatus()
	case *cryptossh.ExitMissingError:
		writeExecJSON(remoteArgs, -1, "missing-exit-status")
		return fmt.Errorf("remote command did not return an exit status (connection dropped?)")
	default:
		writeExecJSON(remoteArgs, -1, err.Error())
		return err
	}
	writeExecJSON(remoteArgs, exit, "")
	if exit != 0 {
		os.Exit(exit)
	}
	return nil
}

// writeExecJSON emits a single-line JSON record to stderr when --output json
// is active. Stays on stderr so the remote command's stdout is unpolluted.
func writeExecJSON(argv []string, exitCode int, errStr string) {
	if !jsonOutput() {
		return
	}
	rec := struct {
		Command  []string `json:"command"`
		ExitCode int      `json:"exitCode"`
		Error    string   `json:"error,omitempty"`
	}{argv, exitCode, errStr}
	b, _ := json.Marshal(rec)
	fmt.Fprintln(os.Stderr, string(b))
}

// quoteRemoteCommand joins argv into a single shell string. Each token is
// single-quoted; embedded single quotes are escaped as '\''. This avoids
// surprises from glob/${var} expansion in the *caller's* shell — the user's
// argv reaches the remote shell verbatim.
func quoteRemoteCommand(argv []string) string {
	var b strings.Builder
	for i, a := range argv {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteByte('\'')
		b.WriteString(strings.ReplaceAll(a, "'", `'\''`))
		b.WriteByte('\'')
	}
	return b.String()
}

