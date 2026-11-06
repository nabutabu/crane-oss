package registry

import (
	"context"
	"errors"

	"github.com/nabutabu/crane-oss/internal/capacity"
	"github.com/nabutabu/crane-oss/internal/hostrpc"
	"github.com/nabutabu/crane-oss/internal/provider/awscompute"
	"github.com/nabutabu/crane-oss/pkg/api"
	"k8s.io/client-go/kubernetes"
)

// AttachmentExecutor selects the provider from trusted deployment state.
type AttachmentExecutor struct {
	Store      *capacity.Store
	Kubernetes kubernetes.Interface
	ClusterCA  []byte
	Caller     hostrpc.Caller
}

func (e AttachmentExecutor) provider(ctx context.Context, op api.Operation) (capacity.BootstrapExecutor, error) {
	if e.Store == nil || op.Kind != "attach_zone" {
		return nil, errors.New("attachment executor is unavailable")
	}
	deployment, err := e.Store.LoadDeploymentRuntime(ctx, op.ClusterID)
	if err != nil {
		return nil, err
	}
	switch deployment.Provider {
	case "aws":
		return &awscompute.AttachmentExecutor{Store: e.Store, Kubernetes: e.Kubernetes, ClusterCA: e.ClusterCA, Caller: e.Caller}, nil
	default:
		return nil, errors.New("attachment provider is unavailable")
	}
}

func (e AttachmentExecutor) Step(ctx context.Context, op api.Operation, phase api.ZoneBootstrapPhase) (api.BootstrapStepResult, error) {
	executor, err := e.provider(ctx, op)
	if err != nil {
		return api.BootstrapStepResult{}, err
	}
	return executor.Step(ctx, op, phase)
}

func (e AttachmentExecutor) Validated(ctx context.Context, op api.Operation) (bool, error) {
	executor, err := e.provider(ctx, op)
	if err != nil {
		return false, err
	}
	return executor.Validated(ctx, op)
}
