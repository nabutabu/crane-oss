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

func (n *NoopProvider) DrainHost(ctx context.Context, hostID string) error {
	log.Println("/NoopProvider/DrainHost")
	return nil
}

func (n *NoopProvider) TerminateHost(ctx context.Context, hostID string) error {
	log.Println("/NoopProvider/TerminateHost")
	return nil
}

func (n *NoopProvider) ProvisionHost(ctx context.Context, role string) (string, error) {
	log.Println("/NoopProvider/ProvisionHost")
	return "noop-host-id", nil
}
