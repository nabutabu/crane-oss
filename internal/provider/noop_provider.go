package provider

import (
	"context"
	"log/slog"
)

type NoopProvider struct {
}

func NewNoopProvider() *NoopProvider {
	return &NoopProvider{}
}

func (n *NoopProvider) DrainHost(ctx context.Context, hostID string) error {
	slog.Debug("noop provider draining host", "host_id", hostID)
	return nil
}

func (n *NoopProvider) TerminateHost(ctx context.Context, hostID string) error {
	slog.Debug("noop provider terminating host", "host_id", hostID)
	return nil
}

func (n *NoopProvider) ProvisionHost(ctx context.Context, role string) (string, error) {
	slog.Debug("noop provider provisioning host", "role", role)
	return "noop-host-id", nil
}
