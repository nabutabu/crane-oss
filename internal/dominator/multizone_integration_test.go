package dominator

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/nabutabu/crane-oss/internal/capacity"
	"github.com/nabutabu/crane-oss/internal/provider"
	"github.com/nabutabu/crane-oss/pkg/api"
	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

func multizoneDatabase(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("CRANE_TEST_DATABASE_URL")
	if testing.Short() || dsn == "" {
		t.Skip("requires dedicated local PostgreSQL fixture")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid test database configuration")
	}
	admin := stdlib.OpenDB(*cfg)
	schema := "worker42_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		admin.Close()
		t.Fatal("cannot create isolated test schema")
	}
	cfg.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*cfg)
	t.Cleanup(func() {
		db.Close()
		if _, err := admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); err != nil {
			t.Error("cannot clean isolated test schema")
		}
		admin.Close()
	})
	if err = capacity.InitializeSchema(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return db
}

type multizoneProvider struct {
	instances map[string]provider.RuntimeHost
	failFirst bool
	forged    bool
}

func (p *multizoneProvider) LaunchWorker(_ context.Context, op api.Operation) (provider.RuntimeHost, error) {
	if host, ok := p.instances[op.HostID]; ok {
		return host, nil
	}
	host := provider.RuntimeHost{ProviderID: "instance-" + op.HostID, PrivateIP: fmt.Sprintf("10.0.0.%d", 10+len(p.instances)), KubernetesProviderID: "synthetic:///" + op.ProviderZoneID + "/" + op.HostID}
	p.instances[op.HostID] = host
	if p.failFirst {
		p.failFirst = false
		return provider.RuntimeHost{}, errors.New("injected unknown provider response")
	}
	return host, nil
}
func (p *multizoneProvider) VerifyWorker(_ context.Context, op api.Operation, id string) (provider.RuntimeIdentity, error) {
	host, ok := p.instances[op.HostID]
	if !ok || host.ProviderID != id || p.forged {
		return provider.RuntimeIdentity{}, errors.New("synthetic provider identity mismatch")
	}
	return provider.RuntimeIdentity{PrivateIP: host.PrivateIP, KubernetesProviderID: host.KubernetesProviderID}, nil
}
func (*multizoneProvider) ReconcileSubdRegistration(context.Context, api.Operation, string) error {
	return nil
}
func (*multizoneProvider) ConfigureSubd(context.Context, api.Operation, string, string) error {
	return nil
}

type simulatedWorker struct {
	join        api.ClusterJoinRequest
	key         *ecdsa.PrivateKey
	certificate []byte
	status      api.HostAgentStatus
}
type multizoneRPC struct {
	kube    kubernetes.Interface
	ca      *x509.Certificate
	caKey   *ecdsa.PrivateKey
	workers map[string]*simulatedWorker
}

func (r *multizoneRPC) Call(ctx context.Context, endpoint, identity, method, path string, in, out any) (int, error) {
	worker := r.workers[endpoint]
	switch path {
	case "/v1/health":
		if worker == nil {
			*out.(*api.HostAgentStatus) = api.HostAgentStatus{Identity: identity, Ready: true}
		} else {
			*out.(*api.HostAgentStatus) = worker.status
		}
		return 200, nil
	case "/v1/cluster-join":
		req := in.(api.ClusterJoinRequest)
		if req.Registration.Validate() != nil || req.Registration.ClusterID != "shared" {
			return 409, nil
		}
		if worker == nil {
			key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			leaf := &x509.Certificate{SerialNumber: big.NewInt(int64(len(r.workers) + 2)), Subject: pkix.Name{CommonName: "system:node:" + req.Registration.NodeName, Organization: []string{"system:nodes"}}, NotBefore: r.ca.NotBefore, NotAfter: r.ca.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
			der, err := x509.CreateCertificate(rand.Reader, leaf, r.ca, &key.PublicKey, r.caKey)
			if err != nil {
				return 500, err
			}
			worker = &simulatedWorker{join: req, key: key, certificate: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), status: api.HostAgentStatus{Identity: identity, Ready: true}}
			r.workers[endpoint] = worker
			taints := []core.Taint{{Key: api.ProvisioningTaint, Value: "pending", Effect: core.TaintEffectNoSchedule}}
			for _, taint := range req.Registration.Scheduling.Taints {
				taints = append(taints, core.Taint{Key: taint.Key, Value: taint.Value, Effect: core.TaintEffect(taint.Effect)})
			}
			ip, err := subdPrivateIP(endpoint)
			if err != nil {
				return 500, err
			}
			node := &core.Node{ObjectMeta: metav1.ObjectMeta{Name: req.Registration.NodeName, UID: types.UID(req.Registration.HostID), Labels: api.SchedulingLabels(req.Registration.Scheduling, req.Registration.NodePoolID)}, Spec: core.NodeSpec{ProviderID: req.Registration.ProviderID, Taints: taints, Unschedulable: true}, Status: core.NodeStatus{Addresses: []core.NodeAddress{{Type: core.NodeInternalIP, Address: ip}}, Conditions: []core.NodeCondition{{Type: core.NodeReady, Status: core.ConditionTrue}, {Type: core.NodeNetworkUnavailable, Status: core.ConditionFalse}}}}
			if _, err = r.kube.CoreV1().Nodes().Create(ctx, node, metav1.CreateOptions{}); err != nil {
				return 500, err
			}
			controller := true
			pod := &core.Pod{ObjectMeta: metav1.ObjectMeta{Name: "cilium-" + node.Name, Namespace: "kube-system", Labels: map[string]string{"k8s-app": "cilium"}, OwnerReferences: []metav1.OwnerReference{{Kind: "DaemonSet", UID: "cilium-ds", Controller: &controller}}}, Spec: core.PodSpec{NodeName: node.Name, Containers: []core.Container{{Name: "cilium-agent"}}}, Status: core.PodStatus{Phase: core.PodRunning, Conditions: []core.PodCondition{{Type: core.PodReady, Status: core.ConditionTrue}}, ContainerStatuses: []core.ContainerStatus{{Name: "cilium-agent", Ready: true}}}}
			if _, err = r.kube.CoreV1().Pods("kube-system").Create(ctx, pod, metav1.CreateOptions{}); err != nil {
				return 500, err
			}
		} else if api.JoinFingerprint(req) != api.JoinFingerprint(worker.join) {
			return 409, nil
		}
		*out.(*api.ClusterJoinResult) = api.ClusterJoinResult{OperationID: req.OperationID, Outcome: "applied"}
		return 200, nil
	case "/v1/expected-state":
		req := in.(api.ExpectedHostState)
		worker.status.AcceptedRevision = req.Revision
		worker.status.AppliedRevision = req.Revision
		worker.status.Outcome = "applied"
		worker.status.ExpectedVersion = req.Version
		*out.(*api.HostAgentStatus) = worker.status
		return 200, nil
	case "/v1/cluster-membership":
		req := in.(api.ClusterMembershipRequest)
		reg := worker.join.Registration
		node, err := r.kube.CoreV1().Nodes().Get(ctx, reg.NodeName, metav1.GetOptions{})
		if err != nil {
			return 500, err
		}
		proof := api.ClusterMembership{ContractHash: api.JoinFingerprint(worker.join), NodeName: node.Name, NodeUID: string(node.UID), Certificate: worker.certificate}
		sum := sha256.Sum256(api.MembershipProofData(req.Challenge, proof))
		proof.Signature, err = ecdsa.SignASN1(rand.Reader, worker.key, sum[:])
		if err != nil {
			return 500, err
		}
		*out.(*api.ClusterMembership) = proof
		return 200, nil
	}
	return 404, nil
}

func TestPostgresTwoZoneWorkerAdmissionAndRestart(t *testing.T) {
	db := multizoneDatabase(t)
	store := capacity.NewStore(db)
	ctx := context.Background()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "synthetic-cluster-root"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	pin, _ := api.KubernetesCAHash(caPEM)
	kube := fake.NewClientset(&core.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "crane-cluster-identity", Namespace: "kube-system"}, Data: map[string]string{"cluster_id": "shared"}}, &apps.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "cilium", Namespace: "kube-system", UID: "cilium-ds"}})
	provider := &multizoneProvider{instances: map[string]provider.RuntimeHost{}, failFirst: true}
	rpc := &multizoneRPC{kube: kube, ca: ca, caKey: caKey, workers: map[string]*simulatedWorker{}}
	resolver, err := NewPolicyResolver("../../policy/os.star")
	if err != nil {
		t.Fatal(err)
	}
	controllers := []capacity.ProvisionController{}
	for i, az := range []string{"a", "b"} {
		zone := api.ZoneInput{ID: "zone-" + az, Provider: "aws", AccountID: "synthetic-account", Region: "us-west-2", ProviderZoneID: "us-west-2" + az}
		group := api.NodeGroupInput{Naming: api.NodeGroupNaming{Provider: "aws", DevelopmentStage: "production", Region: "us-west-2", AZ: az, Group: []string{"general-cluster-west", "memory-cluster-west"}[i]}, ClusterID: "shared", ZoneID: zone.ID, NodePoolID: "shared-pool", CapacityClass: "general", ProviderZoneID: zone.ProviderZoneID, MaxNodes: 4, TemplateRevision: "v1", PerNode: api.Resources{VCPU: 2, MemoryMiB: 4096}, LaunchParameters: []byte(`{"synthetic":true}`), Scheduling: api.SchedulingTemplate{InstanceType: "synthetic-machine", Architecture: "amd64", Region: zone.Region, Zone: zone.ProviderZoneID}}
		if i == 1 {
			group.Scheduling.Taints = []api.SchedulingTaint{{Key: "workload", Value: "memory", Effect: "NoSchedule"}}
		}
		if err = group.NormalizeID(); err != nil {
			t.Fatal(err)
		}
		membership := api.ClusterZoneInput{ClusterID: "shared", ZoneID: zone.ID, LifecycleState: "ready", AccessState: "approved"}
		if i == 0 {
			err = store.SeedFirstZone(ctx, api.FirstZoneInput{Zone: zone, Cluster: api.ClusterInput{ID: "shared", APIServerEndpoint: "https://10.0.0.1:6443", CACertHash: pin, KubeadmVersion: "1.35.0", KubeletVersion: "1.35.0", ContainerRuntime: "containerd", CNI: "cilium"}, Membership: membership, Pool: api.NodePoolInput{ID: "shared-pool", Limits: api.Resources{Nodes: 8, VCPU: 16, MemoryMiB: 32768}}, Group: group})
		} else {
			if err = store.CreateZone(ctx, zone); err == nil {
				err = store.CreateClusterZone(ctx, membership)
			}
			if err == nil {
				err = store.CreateNodeGroup(ctx, group)
			}
		}
		if err != nil {
			t.Fatal(err)
		}
		identity := "spiffe://synthetic.test/dominator/" + zone.ID
		if _, err = db.Exec(`INSERT INTO zone_dominators(zone_id,endpoint,identity) VALUES($1,'https://10.0.0.1:44000',$2)`, zone.ID, identity); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(`INSERT INTO host(id,zone,role,state,provider,providerid,protected) VALUES($1,$2,'spire','READY','aws',$3,true)`, "spire-"+az, zone.ID, "spire-id-"+az); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if _, err = db.Exec(`INSERT INTO host(id,zone,role,state,provider,providerid,protected) VALUES('platform-host',$1,'platform','READY','aws','platform-id',true)`, zone.ID); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(`INSERT INTO deployment_runtime(cluster_id,deployment_id,provider,account_id,region,vpc_id,spire_host_id,platform_host_id,dominator_image,runtime_secret_name,trust_domain,pod_cidr)
 VALUES('shared','synthetic-deployment','aws','synthetic-account','us-west-2','vpc-synthetic','spire-a','platform-host','synthetic-dominator','synthetic-runtime','synthetic.test','10.244.0.0/16')`); err != nil {
				t.Fatal(err)
			}
		}
		if _, err = store.SetTarget(ctx, api.CapacityRequest{NodeGroupID: group.ID, ClusterID: "shared", RequestID: "scale-" + az, DesiredTarget: 1, TemplateRevision: "v1"}); err != nil {
			t.Fatal(err)
		}
		executor := &ProvisionExecutor{Store: store, Provider: provider, Kubernetes: kube, Caller: rpc, Resolver: resolver, TrustDomain: "synthetic.test", ClusterID: "shared", ClusterCA: caPEM}
		controllers = append(controllers, capacity.ProvisionController{Store: store, Executor: executor, ZoneID: zone.ID, Identity: identity, Lease: time.Minute})
	}
	// A lost provider response must reuse the same instance on retry.
	if controllers[0].RunOnce(ctx) == nil {
		t.Fatal("expected injected launch uncertainty")
	}
	for i := range controllers {
		if err = controllers[i].RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
		// Restart the controller before enrollment, retaining durable operation identity.
		restarted := controllers[i]
		if err = restarted.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	discovery, err := store.AutoscalerDiscovery(ctx, "shared")
	if err != nil || len(discovery.Groups) != 2 {
		t.Fatal("shared discovery failed", err)
	}
	for _, group := range discovery.Groups {
		if len(group.Nodes) != 1 || group.Nodes[0].State != "CONVERGING" {
			t.Fatal("worker reached READY before validation")
		}
	}
	// A valid host with unhealthy Cilium must remain quarantined.
	nodes, err := kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var nodeB *core.Node
	for i := range nodes.Items {
		if nodes.Items[i].Labels["topology.kubernetes.io/zone"] == "us-west-2b" {
			nodeB = &nodes.Items[i]
		}
	}
	if nodeB == nil {
		t.Fatal("second zone worker missing")
	}
	pod, err := kube.CoreV1().Pods("kube-system").Get(ctx, "cilium-"+nodeB.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pod.Status.ContainerStatuses[0].Ready = false
	if _, err = kube.CoreV1().Pods("kube-system").UpdateStatus(ctx, pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = controllers[1].RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	hostID := string(nodeB.UID)
	host, err := store.Host(ctx, hostID)
	if err != nil || host.State != api.HostConverging {
		t.Fatal("unhealthy Cilium produced READY", err)
	}
	pod.Status.ContainerStatuses[0].Ready = true
	if _, err = kube.CoreV1().Pods("kube-system").UpdateStatus(ctx, pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	// Wrong-cluster credentials, drift and stale expected state also retain quarantine.
	assertConverging := func() {
		t.Helper()
		current, err := store.Host(ctx, hostID)
		if err != nil || current.State != api.HostConverging {
			t.Fatal("failed gate produced READY", err)
		}
		node, err := kube.CoreV1().Nodes().Get(ctx, nodeB.Name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, taint := range node.Spec.Taints {
			if taint.Key == api.ProvisioningTaint {
				found = true
			}
		}
		if !found {
			t.Fatal("failed gate released quarantine")
		}
	}
	executor := controllers[1].Executor.(*ProvisionExecutor)
	executor.ClusterID = "foreign"
	if controllers[1].RunOnce(ctx) == nil {
		t.Fatal("wrong-cluster client admitted worker")
	}
	executor.ClusterID = "shared"
	assertConverging()
	drifted, err := kube.CoreV1().Nodes().Get(ctx, nodeB.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	drifted.Labels["topology.kubernetes.io/zone"] = "forged-zone"
	if _, err = kube.CoreV1().Nodes().Update(ctx, drifted, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if controllers[1].RunOnce(ctx) == nil {
		t.Fatal("forged topology admitted worker")
	}
	assertConverging()
	drifted.Labels["topology.kubernetes.io/zone"] = "us-west-2b"
	if _, err = kube.CoreV1().Nodes().Update(ctx, drifted, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	worker := rpc.workers[host.SubdEndpoint]
	expectedVersion := worker.status.ExpectedVersion
	worker.status.ExpectedVersion = "stale"
	if err = controllers[1].RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	assertConverging()
	worker.status.ExpectedVersion = expectedVersion
	// Provider identity failure is retryable and cannot release the worker.
	provider.forged = true
	if controllers[1].RunOnce(ctx) == nil {
		t.Fatal("forged provider identity accepted")
	}
	provider.forged = false
	for i := range controllers {
		if err = controllers[i].RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	discovery, err = store.AutoscalerDiscovery(ctx, "shared")
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range discovery.Groups {
		if len(group.Nodes) != 1 || group.Nodes[0].State != "READY" {
			t.Fatal("validated worker did not reach READY")
		}
		node, err := kube.CoreV1().Nodes().Get(ctx, api.WorkerNodeName(group.Nodes[0].HostID), metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for key, want := range api.SchedulingLabels(group.Scheduling, group.Pool) {
			if node.Labels[key] != want {
				t.Fatal("real Node and template differ")
			}
		}
		for _, taint := range node.Spec.Taints {
			if taint.Key == api.ProvisioningTaint {
				t.Fatal("quarantine not released")
			}
		}
		if !node.Spec.Unschedulable {
			t.Fatal("operator cordon was cleared")
		}
		if group.Scheduling.Zone == "us-west-2b" && len(node.Spec.Taints) != 1 {
			t.Fatal("template taint removed")
		}
	}
	if len(provider.instances) != 2 {
		t.Fatal("retry duplicated provider instances")
	}
	var pools, operations int
	if err = db.QueryRow(`SELECT count(*) FROM node_pools`).Scan(&pools); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM operations WHERE state='succeeded'`).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if pools != 1 || operations != 2 {
		t.Fatal("shared-pool lifecycle did not converge")
	}
}
