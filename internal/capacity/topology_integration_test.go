package capacity

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nabutabu/crane-oss/pkg/api"
)

func testGroup(zone api.ZoneInput, suffix string) api.NodeGroupInput {
	return api.NodeGroupInput{Naming: api.NodeGroupNaming{Provider: zone.Provider, DevelopmentStage: "test", Region: zone.Region, AZ: "b", Group: suffix},
		ClusterID: "cluster", NodePoolID: "pool", ZoneID: zone.ID, ProviderZoneID: zone.ProviderZoneID, CapacityClass: "on-demand", MaxNodes: 8,
		TemplateRevision: "template-1", PerNode: api.Resources{VCPU: 4, MemoryMiB: 8192}, LaunchParameters: []byte(`{"image_id":"ami-pinned"}`)}
}

func TestPostgresMultiZoneMembership(t *testing.T) {
	db := database(t)
	s := NewStore(db)
	ctx := context.Background()
	zones := []api.ZoneInput{
		{ID: "zone-b", Provider: "aws", AccountID: "other-account", Region: "us-east-1", ProviderZoneID: "us-east-1b"},
		{ID: "zone-c", Provider: "fixture-provider", AccountID: "third-account", Region: "other-region", ProviderZoneID: "opaque/native/placement"},
	}
	for _, z := range zones {
		if err := s.CreateZone(ctx, z); err != nil {
			t.Fatal(err)
		}
		g := testGroup(z, "general")
		if err := g.NormalizeID(); err != nil {
			t.Fatal(err)
		}
		if err := s.CreateNodeGroup(ctx, g); !errors.Is(err, ErrInvalid) {
			t.Fatalf("nonmember group: %v", err)
		}
		m := api.ClusterZoneInput{ClusterID: "cluster", ZoneID: z.ID}
		if err := s.CreateClusterZone(ctx, m); err != nil {
			t.Fatal(err)
		}
		if err := s.CreateNodeGroup(ctx, g); err != nil {
			t.Fatal(err)
		}
		req := request("pending", 0, 1)
		req.NodeGroupID = g.ID
		if _, err := s.SetTarget(ctx, req); !errors.Is(err, ErrInactive) {
			t.Fatalf("pending membership: %v", err)
		}
		m.LifecycleState, m.AccessState = "ready", "approved"
		if err := s.UpdateClusterZone(ctx, m); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SetTarget(ctx, req); err != nil {
			t.Fatal(err)
		}
		// Topology fixtures prove storage/admission flexibility, not provider execution.
	}
	mustExec(t, db, `UPDATE cluster_zones SET access_state='revoked' WHERE zone_id='zone-b'`)
	degraded := request("pending", 0, 1)
	degraded.NodeGroupID = "aws-test-us-east-1-b-general"
	if _, err := s.SetTarget(ctx, degraded); err != nil {
		t.Fatalf("lost-response replay after degradation: %v", err)
	}
	degraded.RequestID = "new"
	degraded.ExpectedTargetRevision = 1
	degraded.DesiredTarget = 2
	if _, err := s.SetTarget(ctx, degraded); !errors.Is(err, ErrInactive) {
		t.Fatalf("revoked admission: %v", err)
	}
	if _, err := s.SetTarget(ctx, request("healthy-zone", 0, 1)); err != nil {
		t.Fatal(err)
	}
	if count(t, db, "infrastructure_reservations") != 3 {
		t.Fatal("membership change lost reservations")
	}
	// A second cluster may attach to the same provider zone.
	if err := s.CreateCluster(ctx, api.ClusterInput{ID: "second-cluster", KubeadmVersion: "v1.35.0", KubeletVersion: "v1.35.0", ContainerRuntime: "containerd", CNI: "cilium"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateClusterZone(ctx, api.ClusterZoneInput{ClusterID: "second-cluster", ZoneID: "zone"}); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresRetainedAllocationAndIndependentPools(t *testing.T) {
	db := database(t)
	s := NewStore(db)
	ctx := context.Background()
	mustExec(t, db, `UPDATE node_pools SET max_nodes=1`)
	if _, err := s.SetTarget(ctx, request("up", 0, 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimDominator(ctx, "zone", "provision", "spiffe://test.internal/dominator/zone", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetTarget(ctx, request("down", 1, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetTarget(ctx, request("replacement", 2, 1)); !errors.Is(err, ErrLimit) {
		t.Fatalf("retained allocation bypassed limit: %v", err)
	}
	if err := s.CreateNodePool(ctx, api.NodePoolInput{ID: "independent", Limits: api.Resources{VCPU: 4, MemoryMiB: 8192, Nodes: 1}}); err != nil {
		t.Fatal(err)
	}
	z := api.ZoneInput{ID: "zone", Provider: "aws", Region: "us-west-2", ProviderZoneID: "us-west-2a"}
	g := testGroup(z, "independent")
	g.NodePoolID = "independent"
	g.Naming.AZ = "a"
	if err := g.NormalizeID(); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNodeGroup(ctx, g); err != nil {
		t.Fatal(err)
	}
	req := request("independent", 0, 1)
	req.NodeGroupID = g.ID
	if _, err := s.SetTarget(ctx, req); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresReadinessRequiresMembershipConstraint(t *testing.T) {
	db := database(t)
	mustExec(t, db, `DO $$ DECLARE c text; BEGIN SELECT conname INTO c FROM pg_constraint WHERE conrelid='node_groups'::regclass AND confrelid='cluster_zones'::regclass; EXECUTE format('ALTER TABLE node_groups DROP CONSTRAINT %I',c); END $$`)
	if err := CheckSchema(context.Background(), db); err == nil {
		t.Fatal("schema readiness accepted missing membership constraint")
	}
}

func TestPostgresCrossZonePoolConcurrency(t *testing.T) {
	db := database(t)
	s := NewStore(db)
	ctx := context.Background()
	z := api.ZoneInput{ID: "zone-b", Provider: "aws", AccountID: "account", Region: "us-west-2", ProviderZoneID: "us-west-2b"}
	if err := s.CreateZone(ctx, z); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateClusterZone(ctx, api.ClusterZoneInput{ClusterID: "cluster", ZoneID: z.ID, LifecycleState: "ready", AccessState: "approved"}); err != nil {
		t.Fatal(err)
	}
	g := testGroup(z, "general")
	if err := g.NormalizeID(); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNodeGroup(ctx, g); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `UPDATE node_pools SET max_nodes=1`)
	start := make(chan struct{})
	out := make(chan error, 2)
	var wg sync.WaitGroup
	for _, id := range []string{"aws-test-us-west-2-a-general", g.ID} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			<-start
			req := request(id, 0, 1)
			req.NodeGroupID = id
			_, err := s.SetTarget(ctx, req)
			out <- err
		}(id)
	}
	close(start)
	wg.Wait()
	close(out)
	success, denied := 0, 0
	for err := range out {
		if err == nil {
			success++
		} else if errors.Is(err, ErrLimit) {
			denied++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || denied != 1 || count(t, db, "infrastructure_reservations") != 1 {
		t.Fatalf("success=%d denied=%d", success, denied)
	}
}

func TestPostgresTopologyConflictsAndRetry(t *testing.T) {
	db := database(t)
	s := NewStore(db)
	ctx := context.Background()
	z := api.ZoneInput{ID: "zone-b", Provider: "aws", AccountID: "account", Region: "us-west-2", ProviderZoneID: "us-west-2b"}
	for range 2 {
		if err := s.CreateZone(ctx, z); err != nil {
			t.Fatal(err)
		}
	}
	conflict := z
	conflict.AccountID = "different"
	if err := s.CreateZone(ctx, conflict); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if err := s.CreateClusterZone(ctx, api.ClusterZoneInput{ClusterID: "cluster", ZoneID: z.ID}); err != nil {
		t.Fatal(err)
	}
	g := testGroup(z, "general-west")
	if err := g.NormalizeID(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := s.CreateNodeGroup(ctx, g); err != nil {
			t.Fatal(err)
		}
	}
	changed := g
	changed.NodePoolID = "different"
	if err := s.CreateNodeGroup(ctx, changed); err == nil {
		t.Fatal("merged conflicting ownership")
	}
	changed = g
	changed.Naming.AZ = "b-general"
	changed.Naming.Group = "west"
	// Same concatenated ID, different persisted components; must not merge.
	if err := s.CreateNodeGroup(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("ambiguous naming collision: %v", err)
	}
}

func TestTopologyHTTPAuthenticationValidationAndSafeErrors(t *testing.T) {
	for _, path := range []string{"/v1/cluster-zones", "/v1/cluster-zones/cluster/zone"} {
		method := "POST"
		if strings.Count(path, "/") > 2 {
			method = "PATCH"
		}
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, path, strings.NewReader(`{}`))
		NewAPI(nil, "admin", "", "").Handler().ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatalf("unguarded membership route %d", w.Code)
		}
	}
	for _, body := range []string{`{"id":"pool","team_id":"old","limits":{}}`, `{} {}`} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/v1/node-pools", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer admin")
		NewAPI(nil, "admin", "", "").Handler().ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatal(w.Code)
		}
	}
	db := database(t)
	s := NewStore(db)
	for _, tc := range []struct {
		method, path, body string
		want               int
	}{
		{"POST", "/v1/cluster-zones", `{"cluster_id":"cluster","zone_id":"zone"}`, 409},
		{"PATCH", "/v1/cluster-zones/cluster/zone", `{"lifecycle_state":"failed","access_state":"revoked"}`, 200},
		{"PATCH", "/v1/cluster-zones/cluster/zone", `{"lifecycle_state":"ready"}`, 400},
		{"POST", "/v1/node-groups", `{"naming":{"provider":"aws"}}`, 400},
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		r.Header.Set("Authorization", "Bearer admin")
		NewAPI(s, "admin", "", "").Handler().ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body.String())
		}
	}
}
