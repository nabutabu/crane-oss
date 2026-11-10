package awscompute

import (
	"context"
	"errors"
	"strings"

	"github.com/nabutabu/crane-oss/pkg/api"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// VerifyAttachmentCluster pins an attachment to the existing private cluster.
// The CA and platform host identity come from trusted deployment state.
func VerifyAttachmentCluster(ctx context.Context, kube kubernetes.Interface, ca []byte, op api.Operation, platformInstanceID string) (string, error) {
	invalid := errors.New("attachment Kubernetes identity does not match deployment")
	if kube == nil || op.Kind != "attach_zone" || op.ClusterID == "" || op.CACertHash == "" ||
		platformInstanceID == "" || !instanceIDRE.MatchString(platformInstanceID) {
		return "", invalid
	}
	pin, err := api.KubernetesCAHash(ca)
	if err != nil || pin != op.CACertHash {
		return "", invalid
	}
	marker, err := kube.CoreV1().ConfigMaps("kube-system").Get(ctx, "crane-cluster-identity", metav1.GetOptions{})
	if err != nil || marker.Data["cluster_id"] != op.ClusterID || marker.Immutable == nil || !*marker.Immutable {
		return "", invalid
	}
	nodes, err := kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", errors.New("discover deployment platform node")
	}
	name := ""
	for _, node := range nodes.Items {
		if strings.HasSuffix(node.Spec.ProviderID, "/"+platformInstanceID) && strings.HasPrefix(node.Spec.ProviderID, "aws:///") {
			if name != "" || node.Name == "" {
				return "", invalid
			}
			name = node.Name
		}
	}
	if name == "" {
		return "", invalid
	}
	return name, nil
}
