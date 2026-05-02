package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"text/template"

	"github.com/spf13/cobra"

	rlconfig "github.com/runlease/runlease/pkg/config"
)

var manifestInitOutput string

func init() {
	manifestInitCmd.Flags().StringVarP(&manifestInitOutput, "output", "o", "manifest.yaml", "output file path")
	manifestCmd.AddCommand(manifestInitCmd)
	rootCmd.AddCommand(manifestCmd)
}

var manifestCmd = &cobra.Command{
	Use:   "manifest",
	Short: "Manage runlease manifests",
}

var manifestInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Interactively create a new manifest YAML",
	RunE:  runManifestInit,
}

const manifestTemplate = `name: {{ .Name }}
provider: {{ .Provider }}
region: {{ .Region }}
instanceClass: {{ .InstanceClass }}
baseImage: {{ .BaseImage }}
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
{{- if eq .Provider "aws" }}
# securityGroupIDs:
#   - sg-xxxxx
# subnetID: subnet-xxxxx
# instanceProfile: arn:aws:iam::123456789012:instance-profile/my-profile
{{- end }}
{{- if eq .Provider "gcp" }}
# serviceAccount: my-sa@my-project.iam.gserviceaccount.com
# networkTags:
#   - allow-ssh
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

func runManifestInit(cmd *cobra.Command, _ []string) error {
	out := cmd.OutOrStdout()
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
		fmt.Fprintf(out, "\n  AWS: AMI IDs are region-specific. Find the latest Amazon Linux 2023 AMI with:\n")
		fmt.Fprintf(out, "    aws ssm get-parameter --name /aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-x86_64 --region %s --query Parameter.Value --output text\n", region)
		imagePrompt = "Base image (AMI ID)"
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

	f, err := os.Create(manifestInitOutput)
	if err != nil {
		return fmt.Errorf("creating %s: %w", manifestInitOutput, err)
	}
	defer f.Close()

	if err := tmpl.Execute(f, data); err != nil {
		return fmt.Errorf("writing manifest: %w", err)
	}

	fmt.Fprintf(out, "\n✓ Written to %s\n", manifestInitOutput)
	fmt.Fprintf(out, "  Launch with: runlease up -m %s\n\n", manifestInitOutput)
	return nil
}
