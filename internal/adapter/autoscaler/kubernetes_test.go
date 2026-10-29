package autoscaler

import (
	"context"
	"testing"

	pb "github.com/nabutabu/crane-oss/internal/adapter/autoscaler/protos"
	"github.com/nabutabu/crane-oss/pkg/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

type removalStub struct{ removed []api.NodeRemovalRequest }

func (*removalStub) Target(context.Context, string) (api.CapacityResult, error) {
	return api.CapacityResult{TargetNodes: 2, TargetRevision: 1}, nil
}
func (*removalStub) SetTarget(context.Context, api.CapacityRequest) (api.CapacityResult, error) {
	return api.CapacityResult{}, nil
}
func (s *removalStub) RemoveNodes(_ context.Context, r api.NodeRemovalRequest) error {
	s.removed = append(s.removed, r)
	return nil
}

func TestLiveDeletionVerifiesKubernetesNameAndProviderID(t *testing.T) {
	const id = "aws-test-us-west-2-a-general"
	backend := &removalStub{}
	stub := &discoveryStub{CraneClient: backend, data: api.AutoscalerDiscovery{Groups: []api.AutoscalerGroup{{ID: id, ClusterID: "cluster", Pool: "pool", TemplateRevision: "rev1", MaxNodes: 4, CPU: 4, MemoryMiB: 8192, Scheduling: api.SchedulingTemplate{InstanceType: "m7i.xlarge", Region: "us-west-2", Zone: "us-west-2a"}, Nodes: []api.AutoscalerNode{{HostID: "host", ProviderID: "aws:///us-west-2a/i-123", State: "READY"}}}}}}
	server, err := NewServer(Config{ClusterID: "cluster"}, stub)
	if err != nil {
		t.Fatal(err)
	}
	client := fake.NewSimpleClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a"}, Spec: corev1.NodeSpec{ProviderID: "aws:///us-west-2a/i-123"}})
	server.SetNodeVerifier(&KubernetesNodeVerifier{client: client})
	bad := &pb.NodeGroupDeleteNodesRequest{Id: id, Nodes: []*pb.ExternalGrpcNode{{Name: "wrong", ProviderID: "aws:///us-west-2a/i-123"}}}
	if _, err = server.NodeGroupDeleteNodes(context.Background(), bad); status.Code(err) != codes.Unavailable || len(backend.removed) != 0 {
		t.Fatalf("invalid name accepted: %v", err)
	}
	bad.Nodes[0].Name = "node-a"
	bad.Nodes[0].ProviderID = "aws:///us-west-2a/i-other"
	if _, err = server.NodeGroupDeleteNodes(context.Background(), bad); status.Code(err) != codes.InvalidArgument || len(backend.removed) != 0 {
		t.Fatalf("invalid provider accepted: %v", err)
	}
	bad.Nodes[0].ProviderID = "aws:///us-west-2a/i-123"
	if _, err = server.NodeGroupDeleteNodes(context.Background(), bad); err != nil || len(backend.removed) != 1 || backend.removed[0].Nodes[0].HostID != "host" {
		t.Fatalf("valid deletion: %#v %v", backend.removed, err)
	}
}
