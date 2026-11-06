package awscompute

import (
	"context"
	"testing"

	"github.com/nabutabu/crane-oss/internal/capacity"
	"github.com/nabutabu/crane-oss/pkg/api"
	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestReconcileAttachmentDominatorIsZoneScopedAndRepeatable(t *testing.T) {
	ctx := context.Background()
	kube := fake.NewClientset(&core.Secret{ObjectMeta: metav1.ObjectMeta{Name: "crane-runtime", Namespace: "crane-system"}})
	kube.PrependReactor("create", "services", func(action ktesting.Action) (bool, runtime.Object, error) {
		create := action.(ktesting.CreateAction)
		service := create.GetObject().(*core.Service).DeepCopy()
		service.Spec.ClusterIP = "10.96.0.42"
		return true, service, kube.Tracker().Create(core.SchemeGroupVersion.WithResource("services"), service, service.Namespace)
	})
	op := api.Operation{Kind: "attach_zone", ClusterID: "cluster", ZoneID: "zone-b"}
	deployment := capacity.DeploymentRuntime{ClusterID: "cluster", DominatorImage: "dominator:pin", RuntimeSecretName: "crane-runtime", TrustDomain: "crane.internal", Region: "us-west-2"}
	for i := 0; i < 2; i++ {
		result, err := ReconcileAttachmentDominator(ctx, kube, op, deployment, "platform-node")
		if err != nil || result.DominatorEndpoint != "https://10.96.0.42:44000" || result.DominatorIdentity != "spiffe://crane.internal/dominator/zone-b" {
			t.Fatalf("reconcile %d: %+v: %v", i, result, err)
		}
	}
	serviceAccount, err := kube.CoreV1().ServiceAccounts("crane-system").Get(ctx, "dominator-zone-b", metav1.GetOptions{})
	if err != nil || serviceAccount.AutomountServiceAccountToken == nil || !*serviceAccount.AutomountServiceAccountToken {
		t.Fatal("zone ServiceAccount missing")
	}
	deploymentObject, err := kube.AppsV1().Deployments("crane-system").Get(ctx, "dominator-zone-b", metav1.GetOptions{})
	if err != nil || deploymentObject.Spec.Template.Spec.NodeName != "platform-node" {
		t.Fatal("Dominator is not pinned to protected platform node")
	}
	if _, err := kube.AppsV1().Deployments("crane-system").Update(ctx, &apps.Deployment{ObjectMeta: deploymentObject.ObjectMeta, Spec: apps.DeploymentSpec{Replicas: deploymentObject.Spec.Replicas}}, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := ReconcileAttachmentDominator(ctx, kube, op, deployment, "platform-node"); err == nil {
		t.Fatal("conflicting existing Deployment accepted")
	}
}
