package capacity

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nabutabu/crane-oss/pkg/api"
)

type bootstrapWorkerFake struct {
	phases []api.ZoneBootstrapPhase
	target api.Operation
}

func (f *bootstrapWorkerFake) Step(_ context.Context, op api.Operation, phase api.ZoneBootstrapPhase) (api.BootstrapStepResult, error) {
	f.phases = append(f.phases, phase)
	f.target = op
	switch phase {
	case api.ZoneInfrastructureProvisioning:
		return api.BootstrapStepResult{ProviderHostID: "i-0123456789abcdef0", HostPrivateIP: "10.80.1.20", ImageID: "ami-pinned"}, nil
	case api.ZoneControlPlaneInitializing:
		return api.BootstrapStepResult{APIServerEndpoint: "https://10.80.1.20:6443", CACertHash: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil
	case api.ZoneDominatorInstalling:
		return api.BootstrapStepResult{DominatorEndpoint: "https://10.96.0.42:44000", DominatorIdentity: "spiffe://crane.internal/dominator/worker-zone"}, nil
	default:
		return api.BootstrapStepResult{}, nil
	}
}
func (f *bootstrapWorkerFake) Validated(context.Context, api.Operation) (bool, error) {
	return true, nil
}

type failingBootstrapExecutor struct {
	calls  int
	tokens []string
}

func (f *failingBootstrapExecutor) Step(_ context.Context, op api.Operation, _ api.ZoneBootstrapPhase) (api.BootstrapStepResult, error) {
	f.calls++
	f.tokens = append(f.tokens, op.ProviderToken)
	if f.calls == 1 {
		return api.BootstrapStepResult{}, errors.New("transient provider failure")
	}
	return api.BootstrapStepResult{ProviderHostID: "i-0123456789abcdef0", HostPrivateIP: "10.80.1.20", ImageID: "ami-pinned"}, nil
}
func (f *failingBootstrapExecutor) Validated(context.Context, api.Operation) (bool, error) {
	return true, nil
}

func TestPostgresBootstrapWorkerAdvancesAndRestarts(t *testing.T) {
	db := database(t)
	s := NewStore(db)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `UPDATE clusters SET api_server_endpoint='https://10.80.0.10:6443',ca_cert_hash='sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' WHERE id='cluster'`); err != nil {
		t.Fatal(err)
	}
	zone := api.ZoneInput{ID: "worker-zone", Provider: "aws", AccountID: "account", Region: "us-west-2", ProviderZoneID: "us-west-2b"}
	group := testGroup(zone, "worker")
	if err := group.NormalizeID(); err != nil {
		t.Fatal(err)
	}
	group.ClusterID, group.NodePoolID = "worker-cluster", "worker-pool"
	first := api.FirstZoneInput{Zone: zone, Cluster: api.ClusterInput{ID: "worker-cluster", KubeadmVersion: "v1.35.0", KubeletVersion: "v1.35.0", ContainerRuntime: "containerd", CNI: "cilium"}, Membership: api.ClusterZoneInput{ClusterID: "worker-cluster", ZoneID: zone.ID}, Pool: api.NodePoolInput{ID: "worker-pool", Limits: api.Resources{VCPU: 32, MemoryMiB: 65536, Nodes: 8}}, Group: group}
	params, err := json.Marshal(map[string]any{"first_zone": first})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateBootstrap(ctx, api.BootstrapRequest{Kind: "create_cluster", RequestID: "worker-request", ClusterID: "worker-cluster", ZoneID: zone.ID, Parameters: params}); err != nil {
		t.Fatal(err)
	}
	fake := &bootstrapWorkerFake{}
	for range 6 {
		controller := BootstrapController{Store: s, Executor: fake, Lease: time.Minute}
		if err := controller.RunOnce(ctx, zone.ID, "worker"); err != nil {
			t.Fatal(err)
		}
	}
	if len(fake.phases) != 5 || fake.phases[0] != api.ZoneInfrastructureProvisioning || fake.phases[4] != api.ZoneValidating {
		t.Fatalf("phases %v", fake.phases)
	}
	if fake.target.ClusterID != "worker-cluster" || fake.target.APIServerEndpoint != "https://10.80.1.20:6443" {
		t.Fatalf("untrusted cluster target: %s %s", fake.target.ClusterID, fake.target.APIServerEndpoint)
	}
	var endpoint, identity string
	if err := db.QueryRowContext(ctx, `SELECT endpoint,identity FROM zone_dominators WHERE zone_id=$1`, zone.ID).Scan(&endpoint, &identity); err != nil {
		t.Fatal(err)
	}
	if endpoint != "https://10.96.0.42:44000" || identity != "spiffe://crane.internal/dominator/worker-zone" {
		t.Fatalf("unexpected Dominator route %s %s", endpoint, identity)
	}
	if _, err := s.NextBootstrapZone(ctx); err == nil {
		t.Fatal("completed operation remains runnable")
	}
}

func TestPostgresBootstrapWorkerRetriesFailedStep(t *testing.T) {
	db := database(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `UPDATE clusters SET api_server_endpoint='https://10.80.0.10:6443',ca_cert_hash='sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' WHERE id='cluster'`); err != nil {
		t.Fatal(err)
	}
	_, err := db.ExecContext(ctx, `INSERT INTO operations(id,kind,zone_id,provider_token,parameters) VALUES('retry-op','create_cluster','zone','stable-token','{}');
 INSERT INTO zone_bootstrap_operations(operation_id,zone_id,cluster_id,request_id) VALUES('retry-op','zone','cluster','retry-request')`)
	if err != nil {
		t.Fatal(err)
	}
	fake := &failingBootstrapExecutor{}
	controller := BootstrapController{Store: NewStore(db), Executor: fake, Lease: time.Second}
	if err := controller.RunOnce(ctx, "zone", "first-worker"); err == nil {
		t.Fatal("expected transient failure")
	}
	var phase string
	if err := db.QueryRowContext(ctx, `SELECT phase FROM zone_bootstrap_operations WHERE operation_id='retry-op'`).Scan(&phase); err != nil {
		t.Fatal(err)
	}
	if phase != "Requested" {
		t.Fatalf("failure advanced phase to %s", phase)
	}
	time.Sleep(1100 * time.Millisecond)
	if err := controller.RunOnce(ctx, "zone", "second-worker"); err != nil {
		t.Fatal(err)
	}
	if fake.calls != 2 || fake.tokens[0] != fake.tokens[1] {
		t.Fatalf("retry calls=%d tokens=%v", fake.calls, fake.tokens)
	}
	var fence int64
	if err := db.QueryRowContext(ctx, `SELECT fence FROM operations WHERE id='retry-op'`).Scan(&fence); err != nil {
		t.Fatal(err)
	}
	if fence != 2 {
		t.Fatalf("retry fence=%d", fence)
	}
}

// Historical attachment operations must remain untouched, even if already queued.
func TestPostgresBootstrapWorkerIgnoresLegacyAttachments(t *testing.T) {
	db := database(t)
	ctx := context.Background()
	_, err := db.ExecContext(ctx, `INSERT INTO operations(id,kind,zone_id,provider_token,parameters) VALUES('legacy-attach','attach_zone','zone','legacy-token','{}');
 INSERT INTO zone_bootstrap_operations(operation_id,zone_id,cluster_id,request_id) VALUES('legacy-attach','zone','cluster','legacy-request')`)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(db)
	if _, err := store.NextBootstrapZone(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("legacy attachment selected: %v", err)
	}
	if _, err := store.NextAttachmentZone(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("legacy attachment selected by API controller: %v", err)
	}
	if _, err := store.Claim(ctx, "zone", "attach_zone", "api-controller", time.Minute); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("legacy attachment was claimable: %v", err)
	}
	fake := &bootstrapWorkerFake{}
	controller := BootstrapController{Store: store, Executor: fake}
	if err := controller.RunOnce(ctx, "zone", "worker"); err != nil {
		t.Fatal(err)
	}
	if err := controller.RunAttachmentOnce(ctx, "zone", "api-controller"); err != nil {
		t.Fatal(err)
	}
	if len(fake.phases) != 0 {
		t.Fatal("legacy attachment executed")
	}
	var state, phase string
	var fence int64
	if err := db.QueryRowContext(ctx, `SELECT o.state,b.phase,o.fence FROM operations o JOIN zone_bootstrap_operations b ON b.operation_id=o.id WHERE o.id='legacy-attach'`).Scan(&state, &phase, &fence); err != nil {
		t.Fatal(err)
	}
	if state != "pending" || phase != "Requested" || fence != 0 {
		t.Fatalf("legacy operation changed: %s %s %d", state, phase, fence)
	}
}
