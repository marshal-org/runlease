package main

import (
	"bufio"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	rlconfig "github.com/marshal-org/runlease/pkg/config"
)

func init() {
	configCmd.AddCommand(configShowCmd)
	configCmd.AddCommand(configSetCmd)
	rootCmd.AddCommand(configCmd)
	rootCmd.AddCommand(configureCmd)
}

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage runlease configuration",
}

var configShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Print current configuration and its file path",
	RunE: func(cmd *cobra.Command, _ []string) error {
		path := defaultConfigPath()
		cfg, err := rlconfig.Load(path)
		if err != nil {
			return err
		}
		out, _ := yaml.Marshal(cfg)
		fmt.Fprintf(cmd.OutOrStdout(), "# %s\n\n%s", path, out)
		return nil
	},
}

var configureCmd = &cobra.Command{
	Use:   "configure",
	Short: "Interactive setup wizard for runlease defaults",
	RunE: func(cmd *cobra.Command, _ []string) error {
		out := cmd.OutOrStdout()
		path := defaultConfigPath()

		cfg, err := rlconfig.Load(path)
		if err != nil {
			return err
		}

		// Exit cleanly on Ctrl+C — Go retries interrupted I/O so the signal
		// would otherwise be swallowed by the blocking scanner.Scan() call.
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		go func() {
			<-sigCh
			fmt.Fprintln(out, "\nCancelled.")
			os.Exit(0)
		}()
		defer signal.Stop(sigCh)

		scanner := bufio.NewScanner(cmd.InOrStdin())
		ask := func(question, def string) string {
			if def != "" {
				fmt.Fprintf(out, "  %s [%s]: ", question, def)
			} else {
				fmt.Fprintf(out, "  %s: ", question)
			}
			if !scanner.Scan() {
				return def
			}
			if v := strings.TrimSpace(scanner.Text()); v != "" {
				return v
			}
			return def
		}

		fmt.Fprintf(out, "Runlease setup. Press Enter to keep existing value.\n\n")

		provDefault := cfg.Defaults.Provider
		if provDefault == "" {
			provDefault = "gcp"
		}
		fmt.Fprintf(out, "  Providers: aws, gcp\n")
		cfg.Defaults.Provider = ask("Default provider", provDefault)

		regionDefault := cfg.Defaults.Region
		if regionDefault == "" {
			regionDefault = map[string]string{"gcp": "us-central1-a", "aws": "us-east-1"}[cfg.Defaults.Provider]
		}
		cfg.Defaults.Region = ask("Default region / zone", regionDefault)

		fmt.Fprintf(out, "\n  Classes: compute.micro, compute.small, compute.medium, compute.large, compute.gpu\n")
		sizeDefault := cfg.Defaults.Size
		if sizeDefault == "" {
			sizeDefault = "compute.small"
		}
		cfg.Defaults.Size = ask("Default instance class", sizeDefault)

		ttlDefault := cfg.Defaults.TTL
		if ttlDefault == "" {
			ttlDefault = "2h"
		}
		cfg.Defaults.TTL = ask("Default TTL", ttlDefault)

		fmt.Fprintln(out)
		cfg.Relay.Addr = ask("Relay address (leave empty to skip)", cfg.Relay.Addr)

		if err := rlconfig.Save(path, cfg); err != nil {
			return err
		}

		cfgYAML, _ := yaml.Marshal(cfg)
		fmt.Fprintf(out, "\n✅ Configuration saved → %s\n", path)
		fmt.Fprintf(out, "──────────────────────────────────────\n")
		fmt.Fprintf(out, "%s", cfgYAML)
		fmt.Fprintf(out, "──────────────────────────────────────\n\n")

		fmt.Fprintf(out, "🚀 Ready to launch your first instance:\n\n")
		if cfg.Relay.Addr != "" {
			fmt.Fprintf(out, "   runlease up\n\n")
			fmt.Fprintf(out, "   # override anything on the fly:\n")
			fmt.Fprintf(out, "   runlease up --size compute.medium --ttl 4h\n\n")
			fmt.Fprintf(out, "📡 Relay configured — connect without a public IP:\n\n")
			fmt.Fprintf(out, "   runlease connect <name>\n")
		} else {
			fmt.Fprintf(out, "   runlease up\n\n")
			fmt.Fprintf(out, "💡 Tip: add a relay to connect without a public IP:\n\n")
			fmt.Fprintf(out, "   runlease config set relay.addr wss://your-relay.example.com\n")
		}
		fmt.Fprintf(out, "\n📋 Need advanced options? (VPC, security groups, custom AMI, service accounts)\n\n")
		fmt.Fprintf(out, "   runlease manifest new      — guided wizard with all available fields\n")
		fmt.Fprintf(out, "   runlease up -m manifest.yaml\n")
		fmt.Fprintln(out)
		return nil
	},
}

var configSetCmd = &cobra.Command{
	Use:   "set <key> <value>",
	Short: "Set a configuration value",
	Long: `Set a configuration value and save it to ~/.runlease/config.yaml.

Valid keys:
  relay.addr          default relay address  (e.g. wss://relay.runlease.online)
  defaults.provider   default provider       (aws or gcp)
  defaults.size       default instance class (e.g. compute.small)
  defaults.ttl        default instance TTL   (e.g. 2h)
  defaults.region     default region/zone    (e.g. us-central1-a)`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		path := defaultConfigPath()
		cfg, err := rlconfig.Load(path)
		if err != nil {
			return err
		}
		if err := cfg.Set(args[0], args[1]); err != nil {
			return err
		}
		if err := rlconfig.Save(path, cfg); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Set %s = %s  (%s)\n", args[0], args[1], path)
		return nil
	},
}
