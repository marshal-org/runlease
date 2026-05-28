package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	cryptossh "golang.org/x/crypto/ssh"

	"github.com/runlease/runlease/pkg/credential"
)

var (
	forwardLocal  []string
	forwardRemote []string
	forwardAll    bool
)

func init() {
	forwardCmd.Flags().StringArrayVarP(&forwardLocal, "local", "L", nil,
		"local forward: [bind:]localPort:remoteHost:remotePort (repeatable)")
	forwardCmd.Flags().StringArrayVarP(&forwardRemote, "remote", "R", nil,
		"remote forward: [bind:]remotePort:localHost:localPort (repeatable)")
	forwardCmd.Flags().BoolVar(&forwardAll, "all", false,
		"apply every forward declared in the manifest (manifest.forwards)")
	rootCmd.AddCommand(forwardCmd)
}

var forwardCmd = &cobra.Command{
	Use:   "forward [name|id]",
	Short: "Open SSH port forwards through the relay/tunnel; blocks until Ctrl+C",
	Long: `Open one or more SSH port forwards over the existing SSH/relay tunnel.

Local forward syntax (-L) — listen on the local machine, traffic goes to
the remote side. Same convention as 'ssh -L':

  -L 6443:localhost:6443                  bind 127.0.0.1:6443 → instance:6443
  -L 0.0.0.0:8080:localhost:80            bind 0.0.0.0:8080   → instance:80

Remote forward syntax (-R) — listen on the instance, traffic goes to the
local side:

  -R 9090:localhost:9090                  instance:9090 → 127.0.0.1:9090

With --all, every entry in manifest.forwards is applied. Both --all and
explicit -L/-R can be combined.

Blocks until Ctrl+C.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runForward,
}

func runForward(cmd *cobra.Command, args []string) error {
	reg, err := loadRegistry()
	if err != nil {
		return err
	}
	nameOrID, err := resolveInstance(cmd, reg, args, "forward")
	if err != nil || nameOrID == "" {
		return err
	}
	entry, err := reg.Get(nameOrID)
	if err != nil {
		return err
	}

	specs := append([]forwardSpec(nil), parseForwardSpecs(forwardLocal, dirLocal)...)
	specs = append(specs, parseForwardSpecs(forwardRemote, dirRemote)...)
	if forwardAll {
		// Manifest forwards are applied via runlease up --attach (Phase 1.3).
		// When invoked stand-alone, we don't know the manifest path, so --all
		// is only effective when used through up --attach. Surface a hint.
		fmt.Fprintln(cmd.OutOrStdout(),
			"hint: --all is honored by 'runlease up --attach'; pass -L/-R here.")
	}
	if len(specs) == 0 {
		return fmt.Errorf("no forwards given — pass -L/-R (see 'runlease forward --help')")
	}

	masterKey, err := credential.LoadOrCreateMasterKey(defaultMasterKeyPath())
	if err != nil {
		return fmt.Errorf("loading master key: %w", err)
	}
	cred, err := credential.Load(entry.CredDir, string(entry.Handle.ID), masterKey)
	if err != nil {
		return fmt.Errorf("loading credential: %w", err)
	}

	var conn net.Conn
	var addr string
	if entry.RelayAddr != "" {
		conn, err = dialRelay(entry.RelayAddr, entry.RelayToken)
		if err != nil {
			return fmt.Errorf("relay: %w", err)
		}
		addr = entry.RelayAddr
	} else if entry.Handle.PublicIP != "" {
		conn, err = net.DialTimeout("tcp", entry.Handle.PublicIP+":22", 10*time.Second)
		if err != nil {
			return fmt.Errorf("dial: %w", err)
		}
		addr = entry.Handle.PublicIP + ":22"
	} else {
		return fmt.Errorf("instance %s has no public IP and no relay configured", entry.Handle.ID)
	}

	client, err := newSSHClient(conn, addr, cred)
	if err != nil {
		return err
	}
	defer client.Close()

	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	return runForwards(ctx, cmd.OutOrStdout(), client, specs)
}

// runForwards starts listeners for every spec and blocks until ctx is done.
// Errors from individual listeners are logged but do not tear down the others.
func runForwards(ctx context.Context, out io.Writer, client *cryptossh.Client, specs []forwardSpec) error {
	var wg sync.WaitGroup
	for _, s := range specs {
		s := s
		switch s.dir {
		case dirLocal:
			l, err := net.Listen("tcp", s.listenAddr())
			if err != nil {
				return fmt.Errorf("listen %s: %w", s.listenAddr(), err)
			}
			fmt.Fprintf(out, "🔀 -L %s → %s (via SSH)\n", s.listenAddr(), s.targetAddr())
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-ctx.Done()
				_ = l.Close()
			}()
			wg.Add(1)
			go func() {
				defer wg.Done()
				acceptLoop(ctx, l, func(c net.Conn) {
					remote, err := client.Dial("tcp", s.targetAddr())
					if err != nil {
						fmt.Fprintf(out, "  ✗ dial %s: %v\n", s.targetAddr(), err)
						c.Close()
						return
					}
					proxyConn(c, remote)
				})
			}()
		case dirRemote:
			l, err := client.Listen("tcp", s.listenAddr())
			if err != nil {
				return fmt.Errorf("remote listen %s: %w", s.listenAddr(), err)
			}
			fmt.Fprintf(out, "🔀 -R %s → %s (instance side)\n", s.listenAddr(), s.targetAddr())
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-ctx.Done()
				_ = l.Close()
			}()
			wg.Add(1)
			go func() {
				defer wg.Done()
				acceptLoop(ctx, l, func(c net.Conn) {
					local, err := net.Dial("tcp", s.targetAddr())
					if err != nil {
						fmt.Fprintf(out, "  ✗ dial %s: %v\n", s.targetAddr(), err)
						c.Close()
						return
					}
					proxyConn(c, local)
				})
			}()
		}
	}
	fmt.Fprintln(out, "Forwards active — press Ctrl+C to stop.")
	<-ctx.Done()
	fmt.Fprintln(out, "Closing forwards...")
	wg.Wait()
	return nil
}

func acceptLoop(ctx context.Context, l net.Listener, handle func(net.Conn)) {
	for {
		c, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// Listener closed externally — exit cleanly.
			return
		}
		go handle(c)
	}
}

// proxyConn pipes bytes between two conns and closes both when either side ends.
func proxyConn(a, b net.Conn) {
	done := make(chan struct{}, 2)
	go func() { io.Copy(a, b); done <- struct{}{} }() //nolint:errcheck
	go func() { io.Copy(b, a); done <- struct{}{} }() //nolint:errcheck
	<-done
	_ = a.Close()
	_ = b.Close()
}

// newSSHClient builds an *ssh.Client from an established net.Conn and the
// instance credential. Shared by forward, push, pull.
func newSSHClient(conn net.Conn, addr string, cred *credential.EphemeralCredential) (*cryptossh.Client, error) {
	signer, err := cryptossh.ParsePrivateKey(cred.PrivateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("parsing agent private key: %w", err)
	}
	hostPub, _, _, _, err := cryptossh.ParseAuthorizedKey(
		[]byte(strings.TrimSuffix(cred.HostPublicKeySSH, "\n")),
	)
	if err != nil {
		return nil, fmt.Errorf("parsing host public key: %w", err)
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
		return nil, err
	}
	conn.SetDeadline(time.Time{}) //nolint:errcheck
	return cryptossh.NewClient(c, chans, reqs), nil
}

// forwardSpec describes one parsed -L/-R argument.
type forwardSpec struct {
	dir        forwardDirection
	bind       string // local bind addr (-L) or remote bind addr (-R); "" means default
	listenPort int
	targetHost string
	targetPort int
}

type forwardDirection int

const (
	dirLocal forwardDirection = iota
	dirRemote
)

func (s forwardSpec) listenAddr() string {
	bind := s.bind
	switch s.dir {
	case dirLocal:
		if bind == "" {
			bind = "127.0.0.1"
		}
	case dirRemote:
		if bind == "" {
			bind = "127.0.0.1"
		}
	}
	return net.JoinHostPort(bind, strconv.Itoa(s.listenPort))
}

func (s forwardSpec) targetAddr() string {
	return net.JoinHostPort(s.targetHost, strconv.Itoa(s.targetPort))
}

// parseForwardSpecs parses ssh-style "-L"/"-R" specifications.
//
// Accepted forms:
//   port:host:port                  bind default (127.0.0.1)
//   bind:port:host:port             explicit bind
//   port:port                       shorthand → port:localhost:port
//
// Invalid entries trigger a fatal error via os.Exit so each forward command
// fails loudly before any listeners are opened.
func parseForwardSpecs(raw []string, dir forwardDirection) []forwardSpec {
	out := make([]forwardSpec, 0, len(raw))
	for _, r := range raw {
		s, err := parseForwardSpec(r, dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid forward %q: %v\n", r, err)
			os.Exit(2)
		}
		out = append(out, s)
	}
	return out
}

func parseForwardSpec(raw string, dir forwardDirection) (forwardSpec, error) {
	parts := strings.Split(raw, ":")
	switch len(parts) {
	case 2:
		p, err := strconv.Atoi(parts[0])
		if err != nil {
			return forwardSpec{}, fmt.Errorf("listen port not an integer: %s", parts[0])
		}
		tp, err := strconv.Atoi(parts[1])
		if err != nil {
			return forwardSpec{}, fmt.Errorf("target port not an integer: %s", parts[1])
		}
		return forwardSpec{dir: dir, listenPort: p, targetHost: "localhost", targetPort: tp}, nil
	case 3:
		p, err := strconv.Atoi(parts[0])
		if err != nil {
			return forwardSpec{}, fmt.Errorf("listen port not an integer: %s", parts[0])
		}
		tp, err := strconv.Atoi(parts[2])
		if err != nil {
			return forwardSpec{}, fmt.Errorf("target port not an integer: %s", parts[2])
		}
		return forwardSpec{dir: dir, listenPort: p, targetHost: parts[1], targetPort: tp}, nil
	case 4:
		p, err := strconv.Atoi(parts[1])
		if err != nil {
			return forwardSpec{}, fmt.Errorf("listen port not an integer: %s", parts[1])
		}
		tp, err := strconv.Atoi(parts[3])
		if err != nil {
			return forwardSpec{}, fmt.Errorf("target port not an integer: %s", parts[3])
		}
		return forwardSpec{dir: dir, bind: parts[0], listenPort: p, targetHost: parts[2], targetPort: tp}, nil
	}
	return forwardSpec{}, fmt.Errorf("expected port:host:port or bind:port:host:port (got %d parts)", len(parts))
}
