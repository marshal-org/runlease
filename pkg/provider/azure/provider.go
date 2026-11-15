package azure

import (
	"context"
	"errors"

	"github.com/marshal-org/runlease/pkg/provider"
)

var ErrNotImplemented = errors.New("azure provider is not implemented")

type Provider struct{}

func New(_ context.Context, _ string) (*Provider, error) {
	return &Provider{}, nil
}

func (p *Provider) RequestSpot(_ context.Context, _ provider.InstanceSpec) (*provider.InstanceHandle, error) {
	return nil, ErrNotImplemented
}

func (p *Provider) Terminate(_ context.Context, _ provider.InstanceID) error {
	return ErrNotImplemented
}

func (p *Provider) Status(_ context.Context, _ provider.InstanceID) (provider.InstanceStatus, error) {
	return provider.StatusUnknown, ErrNotImplemented
}

func (p *Provider) WaitUntilRunning(_ context.Context, _ provider.InstanceID) (*provider.InstanceHandle, error) {
	return nil, ErrNotImplemented
}
