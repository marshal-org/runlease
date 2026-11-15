package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/marshal-org/runlease/pkg/provider"
	awsprovider "github.com/marshal-org/runlease/pkg/provider/aws"
	azureprovider "github.com/marshal-org/runlease/pkg/provider/azure"
	gcpprovider "github.com/marshal-org/runlease/pkg/provider/gcp"
	"github.com/marshal-org/runlease/pkg/registry"
)

var (
	version    = "dev"
	outputMode string
)

func init() {
	rootCmd.PersistentFlags().StringVar(&outputMode, "output", "", "alternative output format: json")
}

// jsonOutput returns true when the user asked for --output json (case-insensitive).
func jsonOutput() bool { return outputMode == "json" || outputMode == "JSON" }

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
  runlease manifest new        interactive manifest wizard (alias: init)
  runlease catalog             list available instance classes`,
}

func runleaseHome() string {
	if h := os.Getenv("RUNLEASE_HOME"); h != "" {
		return h
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".runlease")
}

func defaultCredDir() string       { return filepath.Join(runleaseHome(), "credentials") }
func defaultRegistryPath() string  { return filepath.Join(runleaseHome(), "registry.json") }
func defaultMasterKeyPath() string { return filepath.Join(runleaseHome(), "master.key") }
func defaultConfigPath() string    { return filepath.Join(runleaseHome(), "config.yaml") }

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

// newProvider is the central dispatch used by all commands and the lifecycle
// manager. project is GCP-only; empty resolves it from env / gcloud config.
// A var so tests can swap in a fake.
var newProvider = func(ctx context.Context, pt provider.ProviderType, region, project string) (provider.CloudProvider, error) {
	switch pt {
	case provider.ProviderAWS:
		return awsprovider.New(ctx, region)
	case provider.ProviderGCP:
		return gcpprovider.New(ctx, region, project)
	case provider.ProviderAzure:
		return azureprovider.New(ctx, region)
	default:
		return nil, fmt.Errorf("unknown provider %q", pt)
	}
}

// providerForEntry builds the provider for an existing instance, in the GCP
// project recorded at `up` time. Entries written before the project was
// recorded fall back to the env / gcloud project, with a warning on w.
func providerForEntry(ctx context.Context, h provider.InstanceHandle, w io.Writer) (provider.CloudProvider, error) {
	p, err := newProvider(ctx, h.Provider, h.Region, h.Project)
	if err != nil {
		return nil, err
	}
	if h.ProjectUnknown() {
		project := "?"
		if pp, ok := p.(interface{ ProjectID() string }); ok {
			project = pp.ProjectID()
		}
		fmt.Fprintf(w, "⚠️  %s has no recorded GCP project (launched by an older runlease); using %s from GOOGLE_CLOUD_PROJECT / gcloud config.\n",
			h.ManifestName, project)
	}
	return p, nil
}
