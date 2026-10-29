package bootstrap

import (
	"context"
	"fmt"

	"github.com/nabutabu/crane-oss/pkg/api"
)

// OperationSession owns provider-specific state and remote execution for one
// durable cluster or zone operation. Each method must rediscover its inputs so
// a new process can resume the operation after an interrupted remote success.
type OperationSession interface {
	EnsureInfrastructure(context.Context, api.Operation) error
	InitializeControlPlane(context.Context, api.Operation) (api.BootstrapStepResult, error)
	EnsureNetwork(context.Context, api.Operation) error
	EnsureDominator(context.Context, api.Operation) error
	Validate(context.Context, api.Operation) (bool, error)
}

// OperationExecutor adapts provider-neutral phase names to provider
// capabilities. It never interprets cloud identifiers or provisioning data.
type OperationExecutor struct{ Session OperationSession }

func (e OperationExecutor) Step(ctx context.Context, op api.Operation, phase api.ZoneBootstrapPhase) (api.BootstrapStepResult, error) {
	if e.Session == nil {
		return api.BootstrapStepResult{}, fmt.Errorf("bootstrap operation session is not configured")
	}
	switch phase {
	case api.ZoneInfrastructureProvisioning:
		return api.BootstrapStepResult{}, e.Session.EnsureInfrastructure(ctx, op)
	case api.ZoneControlPlaneInitializing:
		if op.Kind != "create_cluster" {
			return api.BootstrapStepResult{}, fmt.Errorf("control plane initialization requires create_cluster")
		}
		return e.Session.InitializeControlPlane(ctx, op)
	case api.ZoneNetworkInstalling:
		return api.BootstrapStepResult{}, e.Session.EnsureNetwork(ctx, op)
	case api.ZoneDominatorInstalling:
		return api.BootstrapStepResult{}, e.Session.EnsureDominator(ctx, op)
	case api.ZoneValidating:
		return api.BootstrapStepResult{}, nil
	default:
		return api.BootstrapStepResult{}, fmt.Errorf("unsupported bootstrap phase %q", phase)
	}
}

func (e OperationExecutor) Validated(ctx context.Context, op api.Operation) (bool, error) {
	if e.Session == nil {
		return false, fmt.Errorf("bootstrap operation session is not configured")
	}
	return e.Session.Validate(ctx, op)
}
