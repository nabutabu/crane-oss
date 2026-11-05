package capacity

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/nabutabu/crane-oss/pkg/api"
)

func TestPostgresCreateIndependentClusterAdmission(t *testing.T) {
	db := database(t)
	s := NewStore(db)
	ctx := context.Background()
	zone := api.ZoneInput{ID: "new-zone", Provider: "aws", AccountID: "other-account", Region: "us-east-1", ProviderZoneID: "us-east-1b"}
	cluster := api.ClusterInput{ID: "second-cluster", KubeadmVersion: "v1.35.0", KubeletVersion: "v1.35.0", ContainerRuntime: "containerd", CNI: "cilium"}
	group := api.NodeGroupInput{Naming: api.NodeGroupNaming{Provider: "aws", DevelopmentStage: "test", Region: "us-east-1", AZ: "b", Group: "second"}, ClusterID: cluster.ID, ZoneID: zone.ID, NodePoolID: "new-pool", ProviderZoneID: zone.ProviderZoneID, CapacityClass: "on-demand", MaxNodes: 8, TemplateRevision: "pinned", PerNode: api.Resources{VCPU: 4, MemoryMiB: 8192}, LaunchParameters: []byte(`{"image_id":"ami-pinned"}`)}
	if err := group.NormalizeID(); err != nil {
		t.Fatal(err)
	}
	first := api.FirstZoneInput{Zone: zone, Cluster: cluster, Membership: api.ClusterZoneInput{ClusterID: cluster.ID, ZoneID: zone.ID}, Pool: api.NodePoolInput{ID: "new-pool", Limits: api.Resources{VCPU: 32, MemoryMiB: 65536, Nodes: 8}}, Group: group}
	parameters, err := json.Marshal(map[string]any{"first_zone": first})
	if err != nil {
		t.Fatal(err)
	}
	req := api.BootstrapRequest{Kind: "create_cluster", RequestID: "create-second", ClusterID: cluster.ID, ZoneID: zone.ID, Parameters: parameters}
	id, err := s.CreateBootstrap(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.CreateBootstrap(ctx, req)
	if err != nil || replay != id {
		t.Fatalf("replay %s: %v", replay, err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			again, replayErr := s.CreateBootstrap(ctx, req)
			if replayErr == nil && again != id {
				replayErr = ErrReplayMismatch
			}
			results <- replayErr
		}()
	}
	wg.Wait()
	close(results)
	for replayErr := range results {
		if replayErr != nil {
			t.Fatal(replayErr)
		}
	}
	var state string
	if err := db.QueryRowContext(ctx, `SELECT lifecycle_state FROM clusters WHERE id=$1`, cluster.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "initializing" {
		t.Fatalf("new cluster state %s", state)
	}
	if err := db.QueryRowContext(ctx, `SELECT lifecycle_state FROM clusters WHERE id='cluster'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "ready" {
		t.Fatalf("existing cluster state %s", state)
	}
	op, err := s.Claim(ctx, zone.ID, "create_cluster", "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.BootstrapPhase(ctx, op, api.ZoneInfrastructureProvisioning, false); err != nil {
		t.Fatal(err)
	}
	result := api.BootstrapStepResult{APIServerEndpoint: "https://10.80.1.10:6443", CACertHash: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	if err := s.SetClusterDiscovery(ctx, op, result); err != nil {
		t.Fatal(err)
	}
	if err := s.SetClusterDiscovery(ctx, op, result); err != nil {
		t.Fatalf("idempotent discovery: %v", err)
	}
	result.APIServerEndpoint = "https://10.80.1.11:6443"
	if err := s.SetClusterDiscovery(ctx, op, result); err == nil {
		t.Fatal("changed API endpoint accepted")
	}

	for _, phase := range []api.ZoneBootstrapPhase{api.ZoneControlPlaneInitializing, api.ZoneNetworkInstalling, api.ZoneDominatorInstalling, api.ZoneValidating} {
		if err := s.BootstrapPhase(ctx, op, phase, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.BootstrapPhase(ctx, op, api.ZoneReady, false); err == nil {
		t.Fatal("unvalidated cluster activated")
	}
	if err := s.BootstrapPhase(ctx, op, api.ZoneReady, true); err != nil {
		t.Fatal(err)
	}
	var lifecycle, access string
	if err := db.QueryRowContext(ctx, `SELECT lifecycle_state,access_state FROM cluster_zones WHERE cluster_id=$1 AND zone_id=$2`, cluster.ID, zone.ID).Scan(&lifecycle, &access); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "ready" || access != "approved" {
		t.Fatalf("membership %s/%s", lifecycle, access)
	}
}
