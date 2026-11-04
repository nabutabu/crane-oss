package dominator

import (
	"context"
	"github.com/nabutabu/crane-oss/pkg/api"
	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"testing"
)

func TestWorkerNodeRejectsDriftAndMissingQuarantine(t *testing.T) {
	reg := api.WorkerRegistration{NodeName: "worker", ProviderID: "provider:///native/instance", NodePoolID: "shared", Scheduling: api.SchedulingTemplate{Architecture: "amd64", Region: "region", Zone: "zone", InstanceType: "machine", Taints: []api.SchedulingTaint{{Key: "workload", Value: "batch", Effect: "NoSchedule"}}}}
	fixture := func() *core.Node {
		return &core.Node{ObjectMeta: metav1.ObjectMeta{Name: reg.NodeName, Labels: api.SchedulingLabels(reg.Scheduling, reg.NodePoolID)}, Spec: core.NodeSpec{ProviderID: reg.ProviderID, Unschedulable: true, Taints: []core.Taint{{Key: api.ProvisioningTaint, Value: "pending", Effect: core.TaintEffectNoSchedule}, {Key: "workload", Value: "batch", Effect: core.TaintEffectNoSchedule}}}, Status: core.NodeStatus{Addresses: []core.NodeAddress{{Type: core.NodeInternalIP, Address: "10.0.0.8"}}}}
	}
	node := fixture()
	if err := validateWorkerNode(node, reg, "10.0.0.8", false); err != nil {
		t.Fatal(err)
	}
	node.Labels["topology.kubernetes.io/zone"] = "forged"
	if validateWorkerNode(node, reg, "10.0.0.8", false) == nil {
		t.Fatal("forged topology accepted")
	}
	node = fixture()
	node.Spec.ProviderID = "forged"
	if validateWorkerNode(node, reg, "10.0.0.8", false) == nil {
		t.Fatal("forged provider ID accepted")
	}
	node = fixture()
	node.Spec.Taints = node.Spec.Taints[1:]
	if validateWorkerNode(node, reg, "10.0.0.8", false) == nil {
		t.Fatal("premature quarantine removal accepted")
	}
	if err := validateWorkerNode(node, reg, "10.0.0.8", true); err != nil {
		t.Fatal("durable release retry rejected", err)
	}
	node.Spec.Taints = nil
	if validateWorkerNode(node, reg, "10.0.0.8", true) == nil {
		t.Fatal("missing template taint accepted")
	}
	if workerExpectedStateReady(api.HostAgentStatus{Ready: true, AcceptedRevision: 2, AppliedRevision: 2, ExpectedVersion: "v1", Outcome: "applied"}, "v1") {
		t.Fatal("unrequested revision admitted")
	}
}

func TestCiliumRequiresOwnedReadyPodOnTheExpectedNode(t *testing.T) {
	controller := true
	ds := &apps.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "cilium", Namespace: "kube-system", UID: "cilium-ds"}}
	pod := &core.Pod{ObjectMeta: metav1.ObjectMeta{Name: "cilium-worker", Namespace: "kube-system", Labels: map[string]string{"k8s-app": "cilium"}, OwnerReferences: []metav1.OwnerReference{{Kind: "DaemonSet", UID: ds.UID, Controller: &controller}}}, Spec: core.PodSpec{NodeName: "worker", Containers: []core.Container{{Name: "cilium-agent"}}}, Status: core.PodStatus{Phase: core.PodRunning, Conditions: []core.PodCondition{{Type: core.PodReady, Status: core.ConditionTrue}}, ContainerStatuses: []core.ContainerStatus{{Name: "cilium-agent", Ready: true}}}}
	ctx := context.Background()
	client := fake.NewClientset(ds, pod)
	if !ciliumReady(ctx, client, "worker") {
		t.Fatal("healthy Cilium rejected")
	}
	pod.Status.ContainerStatuses[0].Ready = false
	client = fake.NewClientset(ds, pod)
	if ciliumReady(ctx, client, "worker") {
		t.Fatal("unhealthy Cilium accepted")
	}
	pod.Status.ContainerStatuses[0].Ready = true
	pod.OwnerReferences = nil
	client = fake.NewClientset(ds, pod)
	if ciliumReady(ctx, client, "worker") {
		t.Fatal("forged Cilium pod accepted")
	}
}

func TestNodeReadyRejectsUnknownNetwork(t *testing.T) {
	node := &core.Node{Status: core.NodeStatus{Conditions: []core.NodeCondition{{Type: core.NodeReady, Status: core.ConditionTrue}, {Type: core.NodeNetworkUnavailable, Status: core.ConditionUnknown}}}}
	if nodeReady(node) {
		t.Fatal("unknown network condition admitted")
	}
	node.Status.Conditions[1].Status = core.ConditionFalse
	if !nodeReady(node) {
		t.Fatal("healthy network condition rejected")
	}
}
