package dominator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"net/url"
	"time"

	"github.com/nabutabu/crane-oss/internal/capacity"
	"github.com/nabutabu/crane-oss/internal/hostrpc"
	"github.com/nabutabu/crane-oss/internal/provider"
	"github.com/nabutabu/crane-oss/pkg/api"
	"go.opentelemetry.io/otel/trace"
	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
)

type ProvisionExecutor struct {
	Store       *capacity.Store
	Provider    provider.RuntimeProvisioner
	Kubernetes  kubernetes.Interface
	Caller      hostrpc.Caller
	Resolver    *PolicyResolver
	TrustDomain string
	ClusterID   string
	ClusterCA   []byte
}

func (p *ProvisionExecutor) Launch(ctx context.Context, op api.Operation) (capacity.ProvisionResult, error) {
	if err := p.validateCluster(ctx, op); err != nil {
		return capacity.ProvisionResult{}, err
	}
	if p.Provider == nil {
		return capacity.ProvisionResult{}, errors.New("runtime provider unavailable")
	}
	host, err := p.Provider.LaunchWorker(ctx, op)
	if err != nil {
		return capacity.ProvisionResult{}, err
	}
	return capacity.ProvisionResult{ProviderID: host.ProviderID, PrivateIP: host.PrivateIP, KubernetesProviderID: host.KubernetesProviderID}, nil
}

func (p *ProvisionExecutor) EnrollAndJoin(ctx context.Context, op api.Operation, host api.Host) error {
	if err := p.validateCluster(ctx, op); err != nil {
		return err
	}
	if p.Provider == nil || p.Kubernetes == nil || p.Caller == nil || p.TrustDomain == "" || p.Store == nil || host.ProviderID == "" || host.SubdEndpoint == "" {
		return errors.New("runtime host enrollment is not configured")
	}
	if _, err := p.verifiedWorker(ctx, op, host); err != nil {
		return err
	}
	if err := p.Provider.ReconcileSubdRegistration(ctx, op, host.ProviderID); err != nil {
		return err
	}
	ip, err := subdPrivateIP(host.SubdEndpoint)
	if err != nil {
		return err
	}
	if err := p.Provider.ConfigureSubd(ctx, op, host.ProviderID, ip); err != nil {
		return err
	}
	identity := "spiffe://" + p.TrustDomain + "/subd/" + host.ProviderID
	var status api.HostAgentStatus
	code, err := p.Caller.Call(ctx, host.SubdEndpoint, identity, "GET", "/v1/health", nil, &status)
	if err != nil || code != 200 || status.Identity != identity || !status.Ready {
		return errors.New("Subd identity is not available")
	}
	if err := p.joinWorker(ctx, op, host, identity); err != nil {
		return err
	}
	return p.deliverExpectedState(ctx, op, host, identity)
}

func (p *ProvisionExecutor) joinWorker(ctx context.Context, op api.Operation, host api.Host, identity string) error {
	if op.APIServerEndpoint == "" || op.CACertHash == "" {
		return errors.New("trusted cluster discovery is unavailable")
	}
	reg, err := p.registration(ctx, op, host)
	if err != nil {
		return err
	}
	tokenIDBytes := make([]byte, 3)
	tokenSecretBytes := make([]byte, 8)
	if _, err := rand.Read(tokenIDBytes); err != nil {
		return errors.New("cannot create temporary Kubernetes join token")
	}
	if _, err := rand.Read(tokenSecretBytes); err != nil {
		return errors.New("cannot create temporary Kubernetes join token")
	}
	tokenID, tokenSecret := hex.EncodeToString(tokenIDBytes), hex.EncodeToString(tokenSecretBytes)
	name := "bootstrap-token-" + tokenID
	expires := time.Now().UTC().Add(10 * time.Minute).Format(time.RFC3339)
	secret := &v1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kube-system", Labels: map[string]string{"kubernetes.io/cluster-service": "true"}},
		Type: v1.SecretType("bootstrap.kubernetes.io/token"), Data: map[string][]byte{
			"token-id": []byte(tokenID), "token-secret": []byte(tokenSecret), "expiration": []byte(expires),
			"usage-bootstrap-authentication": []byte("true"), "usage-bootstrap-signing": []byte("true"),
			"auth-extra-groups": []byte("system:bootstrappers:crane"),
		}}
	if _, err := p.Kubernetes.CoreV1().Secrets("kube-system").Create(ctx, secret, metav1.CreateOptions{}); err != nil {
		return errors.New("cannot create short-lived Kubernetes join token")
	}
	request := api.ClusterJoinRequest{OperationID: op.ID, APIServerEndpoint: op.APIServerEndpoint,
		Token: tokenID + "." + tokenSecret, CACertHash: op.CACertHash, Registration: reg, Fence: op.Fence}
	approvalCtx, cancelApproval := context.WithCancel(ctx)
	approvalDone := make(chan error, 1)
	go func() {
		err := p.approveWorkerCSRs(approvalCtx, op, tokenID, reg.NodeName)
		if err != nil {
			cancelApproval()
		}
		approvalDone <- err
	}()
	var result api.ClusterJoinResult
	code, callErr := p.Caller.Call(approvalCtx, host.SubdEndpoint, identity, "PUT", "/v1/cluster-join", request, &result)
	cancelApproval()
	approvalErr := <-approvalDone
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	deleteErr := p.Kubernetes.CoreV1().Secrets("kube-system").Delete(cleanupCtx, name, metav1.DeleteOptions{})
	if approvalErr != nil || callErr != nil || code != 200 || result.OperationID != op.ID || result.Outcome != "applied" {
		return errors.New("worker cluster join did not complete")
	}
	if deleteErr != nil {
		return errors.New("temporary Kubernetes join token cleanup failed")
	}
	_, _, err = p.ensureNodeIdentity(ctx, op, host, true)
	return err
}

func (p *ProvisionExecutor) deliverExpectedState(ctx context.Context, op api.Operation, host api.Host, identity string) error {
	binding, err := p.Store.ProvisionBinding(ctx, op)
	if err != nil {
		return err
	}
	request := binding.ExpectedState
	if request.Revision == 0 {
		if p.Resolver == nil {
			return errors.New("host policy resolver unavailable")
		}
		policyHost := &api.Host{ID: host.ID, Zone: op.ProviderZoneID, Role: api.Role{Name: op.CapacityClass, ExpectedImage: op.CapacityClass}}
		desired, err := p.Resolver.Resolve(policyHost)
		if err != nil {
			return errors.New("cannot resolve worker expected state")
		}
		request = api.ExpectedHostState{Revision: 1, ImageID: desired.ImageID, Track: desired.Track, Version: desired.Version, Services: map[string]bool{}}
		for name, service := range desired.Services {
			request.Services[name] = service.Running
		}
		for _, change := range desired.Packages {
			request.Packages = append(request.Packages, api.HostPackageChange{Name: change.Name, Action: "install"})
		}
	}
	request, err = p.Store.PinProvisionExpectedState(ctx, op, request)
	if err != nil {
		return err
	}
	var status api.HostAgentStatus
	code, err := p.Caller.Call(ctx, host.SubdEndpoint, identity, "PUT", "/v1/expected-state", request, &status)
	if err != nil || code != 200 || status.Identity != identity || status.Outcome != "applied" || status.AppliedRevision != request.Revision || status.ExpectedVersion != request.Version {
		return errors.New("worker expected state was not applied")
	}
	return nil
}

func (p *ProvisionExecutor) Ready(ctx context.Context, op api.Operation, host api.Host) (bool, error) {
	_, ready, err := p.readyNode(ctx, op, host)
	return ready, err
}

func (p *ProvisionExecutor) readyNode(ctx context.Context, op api.Operation, host api.Host) (*v1.Node, bool, error) {
	if err := p.validateCluster(ctx, op); err != nil {
		recordWorkerGate(ctx, op, "cluster", false)
		return nil, false, err
	}
	recordWorkerGate(ctx, op, "cluster", true)
	node, ready, err := p.ensureNodeIdentity(ctx, op, host, false)
	if err != nil || !ready {
		recordWorkerGate(ctx, op, "identity_and_registration", false)
		return nil, false, err
	}
	recordWorkerGate(ctx, op, "identity_and_registration", true)
	if !nodeReady(node) {
		recordWorkerGate(ctx, op, "node_network", false)
		return nil, false, nil
	}
	recordWorkerGate(ctx, op, "node_network", true)
	if !ciliumReady(ctx, p.Kubernetes, node.Name) {
		recordWorkerGate(ctx, op, "cilium", false)
		return nil, false, nil
	}
	recordWorkerGate(ctx, op, "cilium", true)
	identity := "spiffe://" + p.TrustDomain + "/subd/" + host.ProviderID
	var status api.HostAgentStatus
	code, err := p.Caller.Call(ctx, host.SubdEndpoint, identity, "GET", "/v1/health", nil, &status)
	if err != nil || code != 200 || status.Identity != identity {
		recordWorkerGate(ctx, op, "subd", false)
		return nil, false, errors.New("worker Subd identity check failed")
	}
	if !workerExpectedStateReady(status, host.DesiredStateVersion) {
		recordWorkerGate(ctx, op, "expected_state", false)
		return nil, false, nil
	}
	recordWorkerGate(ctx, op, "expected_state", true)
	recordWorkerGate(ctx, op, "all", true)
	return node, true, nil
}

// Release is called only while capacity holds the fenced handoff transaction.
func (p *ProvisionExecutor) Release(ctx context.Context, op api.Operation, host api.Host) error {
	if !capacity.ProvisionReleaseAuthorized(ctx, op) {
		return capacity.ErrFenced
	}
	node, ready, err := p.readyNode(ctx, op, host)
	if err != nil {
		return err
	}
	if !ready {
		return errors.New("worker readiness changed before scheduling release")
	}
	taints := node.Spec.Taints[:0]
	for _, taint := range node.Spec.Taints {
		if taint.Key != api.ProvisioningTaint {
			taints = append(taints, taint)
		}
	}
	node.Spec.Taints = taints
	if _, err = p.Kubernetes.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{}); err != nil {
		return errors.New("worker scheduling release conflicted or failed")
	}
	trace.SpanFromContext(ctx).AddEvent("worker provisioning quarantine released")
	return nil
}

func (p *ProvisionExecutor) ensureNodeIdentity(ctx context.Context, op api.Operation, host api.Host, bind bool) (*v1.Node, bool, error) {
	ip, err := subdPrivateIP(host.SubdEndpoint)
	if err != nil {
		return nil, false, err
	}
	proof, err := p.workerMembership(ctx, op, host)
	if err != nil {
		return nil, false, err
	}
	node, err := p.Kubernetes.CoreV1().Nodes().Get(ctx, proof.NodeName, metav1.GetOptions{})
	if err != nil || string(node.UID) != proof.NodeUID {
		return nil, false, errors.New("worker Node UID conflicts with authenticated membership")
	}
	reg, err := p.registration(ctx, op, host)
	if err != nil {
		return nil, false, err
	}
	binding := capacity.ProvisionBinding{NodeName: proof.NodeName, NodeUID: proof.NodeUID, ProviderID: reg.ProviderID, ContractHash: proof.ContractHash}
	if !bind {
		binding, err = p.Store.ProvisionBinding(ctx, op)
		if err != nil || binding.NodeUID != proof.NodeUID || binding.NodeName != proof.NodeName || binding.ProviderID != reg.ProviderID || binding.ContractHash != proof.ContractHash {
			return nil, false, errors.New("worker identity conflicts with durable Node binding")
		}
	}
	if err = validateWorkerNode(node, reg, ip, binding.ReleaseStarted); err != nil {
		return nil, false, err
	}
	annotations := map[string]string{"crane.io/host-id": host.ID, "crane.io/cluster-id": op.ClusterID, "crane.io/node-group-id": op.NodeGroupID, "crane.io/node-pool-id": op.NodePoolID, "crane.io/operation-id": op.ID, "crane.io/template-revision": op.TemplateRevision, "crane.io/registration-contract": proof.ContractHash}
	for key, want := range annotations {
		got := node.Annotations[key]
		if got != want && (!bind || got != "") {
			return nil, false, errors.New("worker ownership annotation drift")
		}
	}
	if bind {
		if err = p.Store.BindProvisionNode(ctx, op, binding); err != nil {
			return nil, false, err
		}
		if node.Annotations == nil {
			node.Annotations = map[string]string{}
		}
		for key, want := range annotations {
			node.Annotations[key] = want
		}
		node, err = p.Kubernetes.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
		if err != nil {
			return nil, false, errors.New("cannot record verified worker ownership")
		}
	}
	return node, true, nil
}

func validateWorkerNode(node *v1.Node, reg api.WorkerRegistration, ip string, releasing bool) error {
	if node.Name != reg.NodeName || node.Spec.ProviderID != reg.ProviderID {
		return errors.New("worker provider identity drift")
	}
	matched := false
	for _, address := range node.Status.Addresses {
		if address.Type == v1.NodeInternalIP && address.Address == ip {
			matched = true
		}
	}
	if !matched {
		return errors.New("worker address conflicts with provider")
	}
	for key, want := range api.SchedulingLabels(reg.Scheduling, reg.NodePoolID) {
		if node.Labels[key] != want {
			return errors.New("worker scheduling label drift")
		}
	}
	for _, want := range reg.Scheduling.Taints {
		found := false
		for _, got := range node.Spec.Taints {
			if got.Key == want.Key && string(got.Effect) == want.Effect && got.Value == want.Value {
				found = true
			}
		}
		if !found {
			return errors.New("worker template taint drift")
		}
	}
	quarantined := false
	for _, got := range node.Spec.Taints {
		if got.Key == api.ProvisioningTaint {
			if got.Value != "pending" || got.Effect != v1.TaintEffectNoSchedule {
				return errors.New("worker quarantine drift")
			}
			quarantined = true
		}
	}
	if !quarantined && !releasing {
		return errors.New("worker provisioning quarantine missing")
	}
	return nil
}

func workerExpectedStateReady(status api.HostAgentStatus, desiredVersion string) bool {
	return desiredVersion != "" && status.Ready && status.AcceptedRevision == 1 &&
		status.AppliedRevision == status.AcceptedRevision && status.Outcome == "applied" &&
		status.ExpectedVersion == desiredVersion
}

func nodeReady(node *v1.Node) bool {
	ready, networkUnavailable := false, false
	for _, condition := range node.Status.Conditions {
		switch condition.Type {
		case v1.NodeReady:
			ready = condition.Status == v1.ConditionTrue
		case v1.NodeNetworkUnavailable:
			networkUnavailable = condition.Status == v1.ConditionTrue
		}
	}
	return ready && !networkUnavailable
}

func ciliumReady(ctx context.Context, client kubernetes.Interface, nodeName string) bool {
	pods, err := client.CoreV1().Pods("kube-system").List(ctx, metav1.ListOptions{LabelSelector: labels.Set{"k8s-app": "cilium"}.AsSelector().String(), FieldSelector: "spec.nodeName=" + nodeName})
	if err != nil || len(pods.Items) != 1 {
		return false
	}
	pod := pods.Items[0]
	ds, err := client.AppsV1().DaemonSets("kube-system").Get(ctx, "cilium", metav1.GetOptions{})
	if err != nil || ds.UID == "" {
		return false
	}
	owned := false
	for _, owner := range pod.OwnerReferences {
		if owner.Kind == "DaemonSet" && owner.UID == ds.UID && owner.Controller != nil && *owner.Controller {
			owned = true
		}
	}
	podReady := false
	for _, condition := range pod.Status.Conditions {
		if condition.Type == v1.PodReady && condition.Status == v1.ConditionTrue {
			podReady = true
		}
	}
	if !owned || !podReady || pod.DeletionTimestamp != nil || pod.Spec.NodeName != nodeName {
		return false
	}
	if pod.Status.Phase != v1.PodRunning || len(pod.Status.ContainerStatuses) == 0 || len(pod.Status.ContainerStatuses) != len(pod.Spec.Containers) {
		return false
	}
	for _, container := range pod.Status.ContainerStatuses {
		if !container.Ready {
			return false
		}
	}
	return true
}

func subdPrivateIP(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("invalid catalog Subd endpoint")
	}
	name := u.Hostname()
	ip := net.ParseIP(name)
	if ip == nil || !ip.IsPrivate() {
		return "", errors.New("invalid catalog Subd endpoint")
	}
	return name, nil
}

func (p *ProvisionExecutor) verifiedWorker(ctx context.Context, op api.Operation, host api.Host) (provider.RuntimeIdentity, error) {
	if p.Provider == nil || host.ID != op.HostID || host.NodeGroupID != op.NodeGroupID || host.NodePoolID != op.NodePoolID || host.Provider != op.Provider {
		return provider.RuntimeIdentity{}, errors.New("worker catalog identity mismatch")
	}
	identity, err := p.Provider.VerifyWorker(ctx, op, host.ProviderID)
	ip, ipErr := subdPrivateIP(host.SubdEndpoint)
	if err != nil || ipErr != nil || identity.PrivateIP != ip || identity.KubernetesProviderID == "" {
		return provider.RuntimeIdentity{}, errors.New("worker provider identity mismatch")
	}
	trace.SpanFromContext(ctx).AddEvent("worker provider identity verified")
	slog.InfoContext(ctx, "worker provider identity verified", "operation_id", op.ID, "host_id", host.ID, "zone_id", op.ZoneID)
	return identity, nil
}
