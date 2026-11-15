package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(ssmCmd)
}

var ssmCmd = &cobra.Command{
	Use:   "ssm [name|id]",
	Short: "Open an SSM Session Manager session to a running instance",
	Long: `Open an interactive session via AWS SSM Session Manager.

Requires:
  - The instance was launched with an IAM instance profile that has the
    AmazonSSMManagedInstanceCore policy attached (set instanceProfile in manifest).
  - The AWS CLI is installed and configured with ssm:StartSession permission.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runSSMSession,
}

func runSSMSession(cmd *cobra.Command, args []string) error {
	reg, err := loadRegistry()
	if err != nil {
		return err
	}
	nameOrID, err := resolveInstance(cmd, reg, args, "ssm")
	if err != nil || nameOrID == "" {
		return err
	}
	entry, err := reg.Get(nameOrID)
	if err != nil {
		return err
	}

	instanceID := string(entry.Handle.ID)
	region := entry.Handle.Region

	if _, err := exec.LookPath("aws"); err != nil {
		return fmt.Errorf("aws CLI not found in PATH — install it to use SSM sessions")
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Opening SSM session to %s (%s)...\n",
		entry.Handle.ManifestName, instanceID)

	c := exec.CommandContext(cmd.Context(), "aws", "ssm", "start-session",
		"--target", instanceID,
		"--region", region,
	)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}
