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
