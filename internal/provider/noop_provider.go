package provider

import (
	"context"
	"log"
)

type NoopProvider struct {
}

func NewNoopProvider() *NoopProvider {
	return &NoopProvider{}
}

func (np *NoopProvider) ProvisionHost(ctx context.Context, spec HostSpec) (string, error) {
	return "232", nil
}

func (np *NoopProvider) DecommissionHost(ctx context.Context, hostID string) error {
	log.Printf("/NoopProvider/DecommissionHost: %s", hostID)
	return nil
}
