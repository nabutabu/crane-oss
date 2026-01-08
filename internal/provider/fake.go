package provider

import (
	"context"
	"errors"
)

type FakeProvider struct {
	FailDrain     bool
	FailTerminate bool
	FailProvision bool
}

func (f *FakeProvider) DrainHost(ctx context.Context, hostID string) error {
	if f.FailDrain {
		return errors.New("fake: drain failed")
	}
	return nil
}

func (f *FakeProvider) TerminateHost(ctx context.Context, hostID string) error {
	if f.FailTerminate {
		return errors.New("fake: terminate failed")
	}
	return nil
}

func (f *FakeProvider) ProvisionHost(ctx context.Context, role string) (string, error) {
	if f.FailProvision {
		return "", errors.New("fake: provision failed")
	}
	return "fake-new-host-id", nil
}
