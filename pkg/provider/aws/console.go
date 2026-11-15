package aws

import (
	"context"
	"encoding/base64"
	"fmt"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"

	"github.com/marshal-org/runlease/pkg/provider"
)

// ConsoleOutput returns the instance's console output. EC2 updates it with a
// delay, so a line written by the boot script can take a while to appear.
func (p *Provider) ConsoleOutput(ctx context.Context, id provider.InstanceID) (string, error) {
	out, err := p.ec2.GetConsoleOutput(ctx, &ec2.GetConsoleOutputInput{
		InstanceId: awssdk.String(string(id)),
		Latest:     awssdk.Bool(true),
	})
	if err != nil {
		return "", fmt.Errorf("GetConsoleOutput: %w", err)
	}
	raw, err := base64.StdEncoding.DecodeString(awssdk.ToString(out.Output))
	if err != nil {
		return "", fmt.Errorf("decoding console output: %w", err)
	}
	return string(raw), nil
}
