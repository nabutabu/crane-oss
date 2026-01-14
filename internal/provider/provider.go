package provider

import (
	"context"
	"time"
)

type InstanceStatus struct {
	SystemStatus   string
	InstanceStatus string
	Events         []InstanceEvent
}
type InstanceEvent struct {
	Code        string
	Description string
	NotBefore   time.Time
}

type Provider interface {
	DrainHost(ctx context.Context, hostID string) error
	TerminateHost(ctx context.Context, hostID string) error
	ProvisionHost(ctx context.Context, role string, id string) (string, error)
	GetProviderName() string
	GetInstanceStatus(ctx context.Context, providerID string) (*InstanceStatus, error)
}
