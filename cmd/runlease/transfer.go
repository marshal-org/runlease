package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/pkg/sftp"
	"github.com/spf13/cobra"

	"github.com/marshal-org/runlease/pkg/credential"
)

func init() {
	rootCmd.AddCommand(pushCmd)
	rootCmd.AddCommand(pullCmd)
}

var pushCmd = &cobra.Command{
	Use:   "push [name] <local-path> <remote-path>",
	Short: "Copy a local file to the instance over the SSH/relay tunnel",
	Args:  cobra.RangeArgs(2, 3),
	RunE:  runPush,
}

var pullCmd = &cobra.Command{
	Use:   "pull [name] <remote-path> <local-path>",
	Short: "Copy a file from the instance to local disk over the SSH/relay tunnel",
	Args:  cobra.RangeArgs(2, 3),
	RunE:  runPull,
}

func runPush(cmd *cobra.Command, args []string) error {
	name, local, remote, err := splitTransferArgs(args, "push")
	if err != nil {
		return err
	}
	client, cleanup, err := openTransferClient(cmd, name)
	if err != nil {
		return err
	}
	defer cleanup()
	src, err := os.Open(local)
	if err != nil {
		return fmt.Errorf("opening local %s: %w", local, err)
	}
	defer src.Close()
	st, err := src.Stat()
	if err != nil {
		return fmt.Errorf("stat local %s: %w", local, err)
	}

	dst, err := client.Create(remote)
	if err != nil {
		return fmt.Errorf("creating remote %s: %w", remote, err)
	}
	defer dst.Close()

	n, err := io.Copy(dst, src)
	if err != nil {
		return fmt.Errorf("transfer: %w", err)
	}
	if err := client.Chmod(remote, st.Mode().Perm()); err != nil {
		// Permission failures are tolerable — they don't break the transfer.
		fmt.Fprintf(cmd.OutOrStdout(), "⚠️  chmod %s on remote failed: %v\n", st.Mode().Perm(), err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "📤 %s → %s (%d bytes)\n", local, remote, n)
	return nil
}

func runPull(cmd *cobra.Command, args []string) error {
	name, remote, local, err := splitTransferArgs(args, "pull")
	if err != nil {
		return err
	}
	client, cleanup, err := openTransferClient(cmd, name)
	if err != nil {
		return err
	}
	defer cleanup()
	src, err := client.Open(remote)
	if err != nil {
		return fmt.Errorf("opening remote %s: %w", remote, err)
	}
	defer src.Close()

	// If the local target is an existing directory, copy into it using the
	// remote file's basename — mirrors scp's behaviour.
	if info, err := os.Stat(local); err == nil && info.IsDir() {
		local = filepath.Join(local, filepath.Base(remote))
	}

	dst, err := os.Create(local)
	if err != nil {
		return fmt.Errorf("creating local %s: %w", local, err)
	}
	defer dst.Close()
	n, err := io.Copy(dst, src)
	if err != nil {
		return fmt.Errorf("transfer: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "📥 %s → %s (%d bytes)\n", remote, local, n)
	return nil
}

// splitTransferArgs accepts either 2 args (auto-resolve instance) or 3 args
// (explicit instance name as first arg).
func splitTransferArgs(args []string, verb string) (name, a, b string, err error) {
	switch len(args) {
	case 2:
		return "", args[0], args[1], nil
	case 3:
		return args[0], args[1], args[2], nil
	default:
		return "", "", "", fmt.Errorf("usage: runlease %s [name] <src> <dst>", verb)
	}
}

// openTransferClient resolves the target instance, dials a tunnel (preferring
// relay), establishes an SFTP subsystem, and returns it along with a cleanup
// function. cleanup is idempotent.
func openTransferClient(cmd *cobra.Command, name string) (*sftp.Client, func(), error) {
	reg, err := loadRegistry()
	if err != nil {
		return nil, nil, err
	}
	var args []string
	if name != "" {
		args = []string{name}
	}
	nameOrID, err := resolveInstance(cmd, reg, args, "transfer")
	if err != nil || nameOrID == "" {
		return nil, nil, err
	}
	entry, err := reg.Get(nameOrID)
	if err != nil {
		return nil, nil, err
	}

	masterKey, err := credential.LoadOrCreateMasterKey(defaultMasterKeyPath())
	if err != nil {
		return nil, nil, fmt.Errorf("loading master key: %w", err)
	}
	cred, err := credential.Load(entry.CredDir, string(entry.Handle.ID), masterKey)
	if err != nil {
		return nil, nil, fmt.Errorf("loading credential: %w", err)
	}

	var conn net.Conn
	var addr string
	if entry.RelayAddr != "" {
		conn, err = openRelay(cmd.Context(), cmd.ErrOrStderr(), entry)
		if err != nil {
			return nil, nil, err
		}
		addr = entry.RelayAddr
	} else if entry.Handle.PublicIP != "" {
		conn, err = net.DialTimeout("tcp", entry.Handle.PublicIP+":22", 10*time.Second)
		if err != nil {
			return nil, nil, fmt.Errorf("dial: %w", err)
		}
		addr = entry.Handle.PublicIP + ":22"
	} else {
		return nil, nil, fmt.Errorf("instance %s has no public IP and no relay configured", entry.Handle.ID)
	}

	sshClient, err := newSSHClient(conn, addr, cred)
	if err != nil {
		return nil, nil, err
	}
	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		sshClient.Close()
		return nil, nil, fmt.Errorf("sftp: %w", err)
	}
	cleanup := func() {
		sftpClient.Close()
		sshClient.Close()
	}
	return sftpClient, cleanup, nil
}
