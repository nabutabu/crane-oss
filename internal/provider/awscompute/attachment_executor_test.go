package awscompute

import (
	"context"
	"net/http"
	"testing"

	"github.com/nabutabu/crane-oss/internal/capacity"
	"github.com/nabutabu/crane-oss/pkg/api"
	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

type attachmentHealthFake struct{ paths []string }

func (f *attachmentHealthFake) Call(_ context.Context, endpoint, identity, method, path string, _, _ any) (int, error) {
	if endpoint != "https://10.96.0.42:44000" || identity != "spiffe://crane.internal/dominator/zone-b" || method != http.MethodGet {
		return http.StatusForbidden, nil
	}
	f.paths = append(f.paths, path)
	return http.StatusOK, nil
}

func TestValidateAttachmentDominatorRequiresReadyIdentityPinnedRoute(t *testing.T) {
	ctx := context.Background()
	kube := fake.NewClientset(
		&apps.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "dominator-zone-b", Namespace: "crane-system", Generation: 1},
			Spec:   apps.DeploymentSpec{Template: core.PodTemplateSpec{Spec: core.PodSpec{NodeName: "platform-node"}}},
			Status: apps.DeploymentStatus{ObservedGeneration: 1, ReadyReplicas: 1, AvailableReplicas: 1}},
		&core.Service{ObjectMeta: metav1.ObjectMeta{Name: "dominator-zone-b", Namespace: "crane-system"}, Spec: core.ServiceSpec{ClusterIP: "10.96.0.42"}},
	)
	caller := &attachmentHealthFake{}
	op := api.Operation{Kind: "attach_zone", ZoneID: "zone-b"}
	deployment := capacity.DeploymentRuntime{TrustDomain: "crane.internal"}
	route := api.BootstrapStepResult{DominatorEndpoint: "https://10.96.0.42:44000", DominatorIdentity: "spiffe://crane.internal/dominator/zone-b"}
	if ok, err := validateAttachmentDominator(ctx, kube, caller, op, deployment, "platform-node", route); err != nil || !ok || len(caller.paths) != 2 {
		t.Fatalf("healthy route: ok=%v paths=%v err=%v", ok, caller.paths, err)
	}
	route.DominatorIdentity = "spiffe://crane.internal/dominator/other-zone"
	if ok, err := validateAttachmentDominator(ctx, kube, caller, op, deployment, "platform-node", route); err == nil || ok {
		t.Fatal("foreign SPIFFE route accepted")
	}
	route.DominatorIdentity = "spiffe://crane.internal/dominator/zone-b"
	workload, err := kube.AppsV1().Deployments("crane-system").Get(ctx, "dominator-zone-b", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	workload.Status.ReadyReplicas = 0
	if _, err := kube.AppsV1().Deployments("crane-system").UpdateStatus(ctx, workload, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if ok, err := validateAttachmentDominator(ctx, kube, caller, op, deployment, "platform-node", route); err == nil || ok {
		t.Fatal("unready Dominator accepted")
	}
}
