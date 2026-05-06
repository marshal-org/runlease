package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/runlease/runlease/pkg/provider"
	awsprovider "github.com/runlease/runlease/pkg/provider/aws"
	azureprovider "github.com/runlease/runlease/pkg/provider/azure"
	gcpprovider "github.com/runlease/runlease/pkg/provider/gcp"
	"github.com/runlease/runlease/pkg/registry"
)

var version = "dev"

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version",
	Run:   func(cmd *cobra.Command, _ []string) { fmt.Fprintln(cmd.OutOrStdout(), version) },
}

func init() { rootCmd.AddCommand(versionCmd) }

var rootCmd = &cobra.Command{
	Use:          "runlease",
	Short:        "Ephemeral spot instance manager",
	SilenceUsage: true,
	Long: `runlease — launch and connect to ephemeral cloud spot instances.

Get started:
  runlease configure           set your defaults (provider, region, relay, etc.)
  runlease up                  launch an instance using your defaults
  runlease up -m manifest.yaml launch from a manifest file
  runlease up --provider gcp   quick launch without a manifest

Manage instances:
  runlease list                show running instances
  runlease connect <name>      SSH via relay (no public IP needed)
  runlease ssh <name>          SSH directly
  runlease down <name>         terminate an instance

Manifest helpers:
  runlease manifest init       interactive manifest wizard
  runlease catalog             list available instance classes`,
}

func defaultCredDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".runlease", "credentials")
}

func defaultRegistryPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".runlease", "registry.json")
}

func defaultMasterKeyPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".runlease", "master.key")
}

func defaultConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".runlease", "config.yaml")
}


func loadRegistry() (*registry.Registry, error) {
	return registry.Load(defaultRegistryPath())
}

// resolveInstance returns the instance name/ID to operate on.
// If args has one element it is used directly. If args is empty, auto-selects
// when exactly one instance is running; prints a pick-list for multiple.
// Returns ("", nil) when the caller should return nil (message already printed).
func resolveInstance(cmd *cobra.Command, reg *registry.Registry, args []string, verb string) (string, error) {
	if len(args) == 1 {
		return args[0], nil
	}
	entries := reg.List()
	switch len(entries) {
	case 0:
		fmt.Fprintln(cmd.OutOrStdout(), "No running instances.")
		return "", nil
	case 1:
		return entries[0].Handle.ManifestName, nil
	default:
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "Multiple instances running — specify one:\n\n")
		for _, e := range entries {
			fmt.Fprintf(out, "  runlease %s %s\n", verb, e.Handle.ManifestName)
		}
		fmt.Fprintln(out)
		return "", nil
	}
}

// newProvider is the central dispatch used by all commands and the lifecycle manager.
func newProvider(ctx context.Context, pt provider.ProviderType, region string) (provider.CloudProvider, error) {
	switch pt {
	case provider.ProviderAWS:
		return awsprovider.New(ctx, region)
	case provider.ProviderGCP:
		return gcpprovider.New(ctx, region)
	case provider.ProviderAzure:
		return azureprovider.New(ctx, region)
	default:
		return nil, fmt.Errorf("unknown provider %q", pt)
	}
}
