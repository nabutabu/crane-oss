package awscompute

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/nabutabu/crane-oss/internal/capacity"
	"github.com/nabutabu/crane-oss/internal/hostrpc"
	"github.com/nabutabu/crane-oss/pkg/api"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// AttachmentExecutor performs AWS and in-cluster steps while capacity owns the
// durable phase graph and final admission transaction.
type AttachmentExecutor struct {
	Store      *capacity.Store
	Kubernetes kubernetes.Interface
	ClusterCA  []byte
	Caller     hostrpc.Caller
}

var _ capacity.BootstrapExecutor = (*AttachmentExecutor)(nil)

func (e *AttachmentExecutor) target(ctx context.Context, op api.Operation) (capacity.DeploymentRuntime, capacity.DeploymentHosts, string, error) {
	if e.Store == nil || e.Kubernetes == nil || op.Kind != "attach_zone" || op.ClusterID == "" {
		return capacity.DeploymentRuntime{}, capacity.DeploymentHosts{}, "", errors.New("attachment executor is not configured")
	}
	deployment, err := e.Store.LoadDeploymentRuntime(ctx, op.ClusterID)
	if err != nil {
		return deployment, capacity.DeploymentHosts{}, "", err
	}
	if deployment.Provider != "aws" {
		return deployment, capacity.DeploymentHosts{}, "", errors.New("unsupported attachment provider")
	}
	hosts, err := e.Store.LoadDeploymentHosts(ctx, deployment)
	if err != nil {
		return deployment, hosts, "", err
	}
	platformNode, err := VerifyAttachmentCluster(ctx, e.Kubernetes, e.ClusterCA, op, hosts.PlatformInstanceID)
	return deployment, hosts, platformNode, err
}

func (e *AttachmentExecutor) Step(ctx context.Context, op api.Operation, phase api.ZoneBootstrapPhase) (api.BootstrapStepResult, error) {
	var result api.BootstrapStepResult
	deployment, hosts, platformNode, err := e.target(ctx, op)
	if err != nil {
		return result, err
	}
	intent, source, err := e.Store.LoadAttachmentSource(ctx, op)
	if err != nil {
		return result, err
	}
	switch phase {
	case api.ZoneInfrastructureProvisioning:
		config, err := ValidateAttachmentPlacement(ctx, intent.Zone, deployment, intent.ProviderConfig)
		if err != nil {
			return result, err
		}
		network, err := EnsureAttachmentNetwork(ctx, deployment, intent.Zone, config)
		if err != nil {
			return result, err
		}
		launch, revision, scheduling, err := ResolveAttachmentTemplate(ctx, deployment, source, intent.Zone, network.PublicSubnetID)
		if err != nil {
			return result, err
		}
		result.LaunchParameters, result.TemplateRevision, result.Scheduling = launch, revision, scheduling
		return result, nil
	case api.ZoneNetworkInstalling:
		// Cluster marker, CA, and protected-node checks above are the empty
		// zone's network preflight. Cilium remains shared with the first zone.
		return result, nil
	case api.ZoneDominatorInstalling:
		if err := ValidateTopology(ctx, intent.Zone); err != nil {
			return result, err
		}
		runtime, err := NewRuntime(ctx, deployment.Region, deployment.TrustDomain)
		if err != nil {
			return result, err
		}
		if err := runtime.ReconcileAttachmentIdentity(ctx, op, deployment, hosts); err != nil {
			return result, err
		}
		return ReconcileAttachmentDominator(ctx, e.Kubernetes, op, deployment, platformNode)
	case api.ZoneValidating:
		return result, nil
	default:
		return result, errors.New("unsupported attachment phase")
	}
}

// Validated requires a ready Deployment and an identity-pinned private health
// call. A pre-existing Service alone cannot activate a zone.
func (e *AttachmentExecutor) Validated(ctx context.Context, op api.Operation) (bool, error) {
	if e.Caller == nil {
		return false, errors.New("attachment health caller is unavailable")
	}
	deployment, _, platformNode, err := e.target(ctx, op)
	if err != nil {
		return false, err
	}
	if _, _, err := e.Store.LoadAttachmentSource(ctx, op); err != nil {
		return false, err
	}
	route, err := e.Store.LoadAttachmentRoute(ctx, op)
	if err != nil {
		return false, err
	}
	return validateAttachmentDominator(ctx, e.Kubernetes, e.Caller, op, deployment, platformNode, route)
}

func validateAttachmentDominator(ctx context.Context, kube kubernetes.Interface, caller hostrpc.Caller, op api.Operation, deployment capacity.DeploymentRuntime, platformNode string, route api.BootstrapStepResult) (bool, error) {
	name := "dominator-" + op.ZoneID
	workload, err := kube.AppsV1().Deployments("crane-system").Get(ctx, name, metav1.GetOptions{})
	if err != nil || workload.Spec.Template.Spec.NodeName != platformNode ||
		workload.Status.ObservedGeneration < workload.Generation || workload.Status.ReadyReplicas != 1 || workload.Status.AvailableReplicas != 1 {
		return false, errors.New("attachment Dominator is not ready")
	}
	service, err := kube.CoreV1().Services("crane-system").Get(ctx, name, metav1.GetOptions{})
	if err != nil || service.Spec.ClusterIP == "" {
		return false, errors.New("attachment Dominator Service is unavailable")
	}
	if route.DominatorEndpoint != "https://"+service.Spec.ClusterIP+":44000" ||
		route.DominatorIdentity != "spiffe://"+deployment.TrustDomain+"/dominator/"+op.ZoneID {
		return false, errors.New("attachment Dominator route does not match deployment")
	}
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for _, path := range []string{"/v1/health", "/v1/db/health"} {
		code, err := caller.Call(probeCtx, route.DominatorEndpoint, route.DominatorIdentity, http.MethodGet, path, nil, nil)
		if err != nil || code != http.StatusOK {
			return false, errors.New("attachment Dominator health is unavailable")
		}
	}
	return true, nil
}
