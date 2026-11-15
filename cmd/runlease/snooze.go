package main

import (
	"encoding/json"
	"fmt"
	"net"
	"time"

	"github.com/spf13/cobra"

	"github.com/marshal-org/runlease/pkg/credential"
)

func init() {
	snoozeCmd.Flags().BoolVar(&snoozeDirect, "direct", false,
		"dial the instance's public IP directly instead of via the relay")
	rootCmd.AddCommand(snoozeCmd)
}

var snoozeDirect bool

var snoozeCmd = &cobra.Command{
	Use:   "snooze [name|id] <duration>",
	Short: "Delay an instance's auto-termination to <duration> from now",
	Long: `Reschedule the instance's self-termination timer to fire <duration> from now,
and update its recorded expiry. <duration> is a Go duration (e.g. 30m, 2h, 90m).

Only works on instances launched with snooze support (the self-termination
timer must be the named runlease-selfterminate unit).

Examples:
  runlease snooze my-runner 2h     # auto-terminate 2h from now
  runlease snooze 45m              # ...resolving the single running instance`,
	Args: cobra.RangeArgs(1, 2),
	RunE: runSnooze,
}

func runSnooze(cmd *cobra.Command, args []string) error {
	reg, err := loadRegistry()
	if err != nil {
		return err
	}

	// Last arg is always the duration; an optional leading arg is the instance.
	var nameOrID, durStr string
	if len(args) == 2 {
		nameOrID, durStr = args[0], args[1]
	} else {
		durStr = args[0]
		if nameOrID, err = resolveInstance(cmd, reg, nil, "snooze"); err != nil || nameOrID == "" {
			return err
		}
	}

	d, err := time.ParseDuration(durStr)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", durStr, err)
	}
	if d < time.Second {
		// The VM takes whole seconds, and 0 would terminate it on the spot.
		return fmt.Errorf("duration must be at least 1s, got %q", durStr)
	}
	secs := int(d.Seconds())

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

	var conn net.Conn
	var addr string
	if entry.RelayAddr != "" && !snoozeDirect {
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

	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("opening SSH session: %w", err)
	}
	defer session.Close()

	out, err := session.CombinedOutput(fmt.Sprintf("sudo /usr/local/bin/runlease-snooze %d", secs))
	if err != nil {
		return fmt.Errorf("rescheduling termination on instance failed "+
			"(is it launched with snooze support?): %s: %w", string(out), err)
	}

	// Reflect the new deadline locally so `list` and the reclaim daemon agree.
	// UTC to match how the rest of the codebase stores timestamps.
	expiresAt := time.Now().UTC().Add(d)
	if uerr := reg.UpdateExpiry(nameOrID, &expiresAt); uerr != nil {
		return fmt.Errorf("instance rescheduled, but updating registry failed: %w", uerr)
	}

	if jsonOutput() {
		rec := struct {
			Name      string `json:"name"`
			ExpiresAt string `json:"expiresAt"`
		}{entry.Handle.ManifestName, expiresAt.UTC().Format(time.RFC3339)}
		b, _ := json.Marshal(rec)
		fmt.Fprintln(cmd.OutOrStdout(), string(b))
		return nil
	}

	fmt.Fprintf(cmd.OutOrStdout(), "😴 %s now auto-terminates in %s (at %s)\n",
		entry.Handle.ManifestName, d.Round(time.Second), expiresAt.Format(time.RFC3339))
	return nil
}
