package provider

import "context"

type HostSpec struct {
	Role string
	// capacity, zone, image, etc — later
}

type Provider interface {
	ProvisionHost(ctx context.Context, spec HostSpec) (string, error)
	DecommissionHost(ctx context.Context, hostID string) error
}
