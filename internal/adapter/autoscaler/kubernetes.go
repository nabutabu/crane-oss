package autoscaler

import (
	"context"
	"os"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// NodeVerifier checks a name against Kubernetes' provider identity. It never
// derives ownership from labels or annotations supplied in the CA request.
type NodeVerifier interface {
	VerifyNode(context.Context, string, string) error
}

type KubernetesNodeVerifier struct{ client kubernetes.Interface }

func NewKubernetesNodeVerifier(kubeconfig string) (*KubernetesNodeVerifier, error) {
	var cfg *rest.Config
	var err error
	if kubeconfig == "" {
		cfg, err = rest.InClusterConfig()
	} else {
		if _, err = os.Stat(kubeconfig); err != nil {
			return nil, status.Error(codes.FailedPrecondition, "Kubernetes credentials unavailable")
		}
		cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	}
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, "Kubernetes credentials unavailable")
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, "Kubernetes client unavailable")
	}
	return &KubernetesNodeVerifier{client: client}, nil
}

func (v *KubernetesNodeVerifier) VerifyNode(ctx context.Context, name, providerID string) error {
	if name == "" || providerID == "" {
		return status.Error(codes.InvalidArgument, "node name and provider identity are required")
	}
	node, err := v.client.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return status.Error(codes.Unavailable, "Kubernetes node verification unavailable")
	}
	if node.Spec.ProviderID != providerID {
		return status.Error(codes.InvalidArgument, "node name and provider identity do not match")
	}
	return nil
}
