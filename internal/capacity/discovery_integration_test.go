package capacity

import (
	"context"
	"testing"

	"github.com/nabutabu/crane-oss/pkg/api"
)

func TestAutoscalerDiscoveryClusterScopeAndDisabledHost(t *testing.T) {
	db := database(t)
	s := NewStore(db)
	ctx := context.Background()
	mustExec(t, db, `UPDATE node_groups SET scheduling='{"instance_type":"m7i.xlarge","architecture":"amd64","region":"us-west-2","zone":"us-west-2a"}'`)
	z := api.ZoneInput{ID: "other-zone", Provider: "aws", AccountID: "account", Region: "us-west-2", ProviderZoneID: "us-west-2b"}
	if err := s.CreateZone(ctx, z); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateClusterZone(ctx, api.ClusterZoneInput{ClusterID: "cluster", ZoneID: z.ID, LifecycleState: "ready", AccessState: "approved"}); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `UPDATE zones SET lifecycle_state='ready' WHERE id='other-zone'`)
	g := api.NodeGroupInput{Naming: api.NodeGroupNaming{Provider: "aws", DevelopmentStage: "test", Region: "us-west-2", AZ: "b", Group: "memory"}, ClusterID: "cluster", NodePoolID: "pool", ZoneID: z.ID, ProviderZoneID: z.ProviderZoneID, CapacityClass: "memory", MaxNodes: 4, TemplateRevision: "template-1", PerNode: api.Resources{VCPU: 8, MemoryMiB: 16384}, LaunchParameters: []byte(`{"image_id":"ami-pinned"}`), Scheduling: api.SchedulingTemplate{InstanceType: "r7i.2xlarge", Architecture: "amd64", Region: "us-west-2", Zone: "us-west-2b"}}
	if err := s.CreateNodeGroup(ctx, g); err != nil {
		t.Fatal(err)
	}
	d, err := s.AutoscalerDiscovery(ctx, "cluster")
	if err != nil || len(d.Groups) != 2 || d.Groups[0].ID != "aws-test-us-west-2-a-general" || d.Groups[1].ID != g.ID || d.Groups[1].Scheduling.Zone != "us-west-2b" {
		t.Fatalf("discovery: %#v %v", d, err)
	}
	mustExec(t, db, `INSERT INTO host(id,zone,crane_zone,node_group_id,node_pool_id,state,provider,providerid) VALUES('host-b','other-zone','other-zone',$1,'pool','READY','aws','i-123')`, g.ID)
	mustExec(t, db, `UPDATE cluster_zones SET access_state='revoked' WHERE zone_id='other-zone'`)
	d, err = s.AutoscalerDiscovery(ctx, "cluster")
	if err != nil || len(d.Groups) != 2 || d.Groups[1].LaunchReady || len(d.Groups[1].Nodes) != 1 || d.Groups[1].Nodes[0].ProviderID != "aws:///us-west-2b/i-123" {
		t.Fatalf("disabled inventory: %#v %v", d, err)
	}
	d, err = s.AutoscalerDiscovery(ctx, "foreign")
	if err != nil || len(d.Groups) != 0 {
		t.Fatalf("foreign discovery: %#v %v", d, err)
	}
}
