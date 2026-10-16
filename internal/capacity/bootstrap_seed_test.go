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
	zone := api.ZoneInput{ID: "zone", Provider: "aws", AccountID: "account", Region: "us-west-2", ProviderZoneID: "us-west-2a"}
	cluster := api.ClusterInput{ID: "cluster", ZoneID: "zone", KubeadmVersion: "v1.35.0", KubeletVersion: "v1.35.0", ContainerRuntime: "containerd", CNI: "cilium"}
	budget := api.Resources{VCPU: 32, MemoryMiB: 65536, Nodes: 8}
	pool := api.NodePoolInput{ID: "pool", TeamID: "team", Limits: budget}
	group := api.NodeGroupInput{ID: "group", ClusterID: "cluster", NodePoolID: "pool", ZoneID: "zone", ProviderZoneID: "us-west-2a", CapacityClass: "general", MaxNodes: 8, TemplateRevision: "template-1", PerNode: api.Resources{VCPU: 4, MemoryMiB: 8192}, LaunchParameters: []byte(`{"image_id":"ami-pinned"}`)}
	for range 2 {
		if err := s.SeedFirstZonePending(ctx, zone, cluster, budget, pool, group); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SetTarget(ctx, request("too-early", 0, 1)); err == nil {
		t.Fatal("unvalidated bootstrap admitted worker capacity")
	}
	for _, table := range []string{"zones", "clusters"} {
		var state string
		if err := db.QueryRow("SELECT lifecycle_state FROM " + table).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state != "initializing" {
			t.Fatalf("%s state %s", table, state)
		}
	}
	if count(t, db, "operations") != 0 || count(t, db, "host") != 0 {
		t.Fatal("bootstrap seed unexpectedly created capacity work")
	}
}
