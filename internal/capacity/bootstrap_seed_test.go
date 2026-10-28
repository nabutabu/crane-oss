package capacity

import (
	"context"
	"testing"

	"github.com/nabutabu/crane-oss/pkg/api"
)

func TestPostgresBootstrapSeedKeepsAdmissionClosed(t *testing.T) {
	db := database(t)
	s := NewStore(db)
	ctx := context.Background()
	zone := api.ZoneInput{ID: "seed-zone", Provider: "aws", AccountID: "account", Region: "us-west-2", ProviderZoneID: "us-west-2b"}
	cluster := api.ClusterInput{ID: "seed-cluster", KubeadmVersion: "v1.35.0", KubeletVersion: "v1.35.0", ContainerRuntime: "containerd", CNI: "cilium"}
	budget := api.Resources{VCPU: 32, MemoryMiB: 65536, Nodes: 8}
	pool := api.NodePoolInput{ID: "pool", Limits: budget}
	group := api.NodeGroupInput{Naming: api.NodeGroupNaming{Provider: "aws", DevelopmentStage: "test", Region: "us-west-2", AZ: "b", Group: "seed"}, ClusterID: "seed-cluster", NodePoolID: "pool", ZoneID: "seed-zone", ProviderZoneID: "us-west-2b", CapacityClass: "general", MaxNodes: 8, TemplateRevision: "template-1", PerNode: api.Resources{VCPU: 4, MemoryMiB: 8192}, LaunchParameters: []byte(`{"image_id":"ami-pinned"}`)}
	if err := group.NormalizeID(); err != nil {
		t.Fatal(err)
	}
	in := api.FirstZoneInput{Zone: zone, Cluster: cluster, Membership: api.ClusterZoneInput{ClusterID: cluster.ID, ZoneID: zone.ID}, Pool: pool, Group: group}
	for range 2 {
		if err := s.SeedFirstZonePending(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	req := request("too-early", 0, 1)
	req.ClusterID = cluster.ID
	req.NodeGroupID = group.ID
	if _, err := s.SetTarget(ctx, req); err == nil {
		t.Fatal("unvalidated bootstrap admitted worker capacity")
	}
	for _, table := range []string{"zones", "clusters"} {
		var state string
		id := zone.ID
		if table == "clusters" {
			id = cluster.ID
		}
		if err := db.QueryRow("SELECT lifecycle_state FROM "+table+" WHERE id=$1", id).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state != "initializing" {
			t.Fatalf("%s state %s", table, state)
		}
	}
	if count(t, db, "operations") != 0 || count(t, db, "host") != 0 {
		t.Fatal("bootstrap seed unexpectedly created capacity work")
	}
	mustExec(t, db, `UPDATE zones SET lifecycle_state='ready' WHERE id='seed-zone'; UPDATE clusters SET lifecycle_state='ready' WHERE id='seed-cluster'; UPDATE cluster_zones SET lifecycle_state='ready',access_state='approved' WHERE cluster_id='seed-cluster'`)
	if _, err := s.SetTarget(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedFirstZonePending(ctx, in); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Target(ctx, cluster.ID, group.ID); err != nil || got.TargetNodes != 1 || got.TargetRevision != 1 {
		t.Fatalf("retry changed target: %+v %v", got, err)
	}
	if count(t, db, "infrastructure_reservations") != 1 {
		t.Fatal("retry changed allocations")
	}
	in.Group.Naming.Group = "other"
	if err := s.SeedFirstZonePending(ctx, in); err == nil {
		t.Fatal("retry accepted conflicting naming")
	}
}
