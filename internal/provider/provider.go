package provider

import "context"

type HostSpec struct {
	Role string
	// capacity, zone, image, etc — later
}

type Provider interface {
	DrainHost(ctx context.Context, hostID string) error
	TerminateHost(ctx context.Context, hostID string) error
	ProvisionHost(ctx context.Context, role string) (string, error)
	GetProviderName() string
}
