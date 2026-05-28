package main

import (
	"bufio"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/template"

	"github.com/spf13/cobra"

	rlconfig "github.com/runlease/runlease/pkg/config"
)

var manifestNewOutput string

func init() {
	manifestNewCmd.Flags().StringVarP(&manifestNewOutput, "output", "o", "", "output file path (default: <instance-name>-manifest.yaml)")
	manifestCmd.AddCommand(manifestNewCmd)
	rootCmd.AddCommand(manifestCmd)
}

var manifestCmd = &cobra.Command{
	Use:   "manifest",
	Short: "Manage runlease manifests",
}

// manifestNewCmd is the canonical name; "init" is kept as an alias so old
// scripts/docs that still say "runlease manifest init" keep working.
var manifestNewCmd = &cobra.Command{
	Use:     "new",
	Aliases: []string{"init"},
	Short:   "Interactively generate a new manifest YAML",
	Long: `Interactively generate a new manifest YAML.

Defaults to writing <instance-name>-manifest.yaml in the current directory.
Pass -o/--output to override.`,
	RunE: runManifestNew,
}

const manifestTemplate = `name: {{ .Name }}
provider: {{ .Provider }}
region: {{ .Region }}
instanceClass: {{ .InstanceClass }}
{{- if .BaseImage }}
baseImage: {{ .BaseImage }}
{{- else }}
# baseImage: (auto-detected at launch)
{{- end }}
reclaim:
  ttl: {{ .TTL }}
{{- if .RelayAddr }}
relay:
  addr: {{ .RelayAddr }}
{{- end }}
# Optional fields (uncomment to use):
# publicIp: true
# tags:
#   env: dev
#   team: platform
#
# sudo: passwordless           # NOPASSWD: ALL for runlease-agent (default: locked)
#
# setup:                       # scripts run as root after bootstrap; output
#   - name: install            # streams to /var/log/runlease-setup.log; touched
#     run: |                   # /run/runlease-setup-done on success
#       curl -sfL https://get.k3s.io | sh -
#   # - name: seed
#   #   file: ./scripts/seed.sh  # local file inlined at launch time
#
# forwards:                    # auto-applied by 'runlease up --attach' and
#   - name: app                # 'runlease forward --all <name>'
#     local: 8080              # direction: local (default) or remote
#     remote: 80               # host: localhost (default)
{{- if eq .Provider "aws" }}
#
# aws:
#   securityGroupIDs:
#     - sg-xxxxx
#   subnetID: subnet-xxxxx
#   instanceProfile: arn:aws:iam::123456789012:instance-profile/my-profile
{{- end }}
{{- if eq .Provider "gcp" }}
#
# gcp:
#   serviceAccount: my-sa@my-project.iam.gserviceaccount.com
#   networkTags:
#     - allow-ssh
{{- end }}
`

type manifestTemplateData struct {
	Name          string
	Provider      string
	Region        string
	InstanceClass string
	BaseImage     string
	TTL           string
	RelayAddr     string
}

func runManifestNew(cmd *cobra.Command, _ []string) error {
	out := cmd.OutOrStdout()

	// Exit cleanly on Ctrl+C — Go retries interrupted I/O, so the signal
	// would otherwise be swallowed by the blocking scanner.Scan() call.
	// (Same pattern as `runlease configure`.)
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
		val := strings.TrimSpace(scanner.Text())
		if val == "" {
			return def
		}
		return val
	}

	cfg, _ := rlconfig.Load(defaultConfigPath())
	if cfg == nil {
		cfg = &rlconfig.Config{}
	}

	fmt.Fprintf(out, "Creating a new runlease manifest. Press Enter to accept defaults.\n\n")

	name := ask("Instance name", "my-runner")

	fmt.Fprintf(out, "\n  Providers: aws, gcp\n")
	provDefault := cfg.Defaults.Provider
	if provDefault == "" {
		provDefault = "gcp"
	}
	prov := ask("Provider", provDefault)

	regionDefault := cfg.Defaults.Region
	if regionDefault == "" {
		regionDefault = map[string]string{"gcp": "us-central1-a", "aws": "us-east-1"}[prov]
	}
	region := ask("Region / zone", regionDefault)

	fmt.Fprintf(out, "\n  Classes: compute.micro, compute.small, compute.medium, compute.large, compute.gpu\n")
	sizeDefault := cfg.Defaults.Size
	if sizeDefault == "" {
		sizeDefault = "compute.small"
	}
	class := ask("Instance class", sizeDefault)

	imageDefault := map[string]string{
		"gcp": "projects/debian-cloud/global/images/family/debian-12",
		"aws": "",
	}[prov]
	var imagePrompt string
	if prov == "aws" {
		fmt.Fprintf(out, "\n  AWS: leave blank to auto-detect the latest Amazon Linux 2023 AMI at launch time.\n")
		imagePrompt = "Base image (AMI ID, or Enter to auto-detect)"
	} else {
		imagePrompt = "Base image"
	}
	image := ask(imagePrompt, imageDefault)

	fmt.Fprintln(out)
	relayAddr := ask("Relay address (leave empty to skip)", cfg.Relay.Addr)

	ttlDefault := cfg.Defaults.TTL
	if ttlDefault == "" {
		ttlDefault = "2h"
	}
	ttl := ask("TTL", ttlDefault)

	data := manifestTemplateData{
		Name:          name,
		Provider:      prov,
		Region:        region,
		InstanceClass: class,
		BaseImage:     image,
		TTL:           ttl,
		RelayAddr:     relayAddr,
	}

	tmpl, err := template.New("manifest").Parse(manifestTemplate)
	if err != nil {
		return err
	}

	// Resolve the output filename: explicit --output wins; otherwise derive
	// from the entered instance name so multiple manifests can sit side-by-side
	// in one project directory without colliding on `manifest.yaml`.
	outputPath := manifestNewOutput
	if outputPath == "" {
		outputPath = name + "-manifest.yaml"
	}

	f, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("creating %s: %w", outputPath, err)
	}
	defer f.Close()

	if err := tmpl.Execute(f, data); err != nil {
		return fmt.Errorf("writing manifest: %w", err)
	}

	fmt.Fprintf(out, "\n✓ Written to %s\n", outputPath)
	fmt.Fprintf(out, "  Launch with: runlease up -m %s\n\n", outputPath)
	return nil
}
