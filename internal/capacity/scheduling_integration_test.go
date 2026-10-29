package capacity

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nabutabu/crane-oss/pkg/api"
)

func TestPostgresTemplateUpdateRefreshAndReplay(t *testing.T) {
	db := database(t)
	s := NewStore(db)
	ctx := context.Background()
	const id = "aws-test-us-west-2-a-general"
	tpl := api.SchedulingTemplate{InstanceType: "m7i.xlarge", Architecture: "amd64", Region: "us-west-2", Zone: "us-west-2a"}
	launch := []byte(`{"image_id":"ami-next","instance_type":"m7i.xlarge"}`)
	if err := s.UpdateNodeGroupScheduling(ctx, id, "template-1", "template-2", tpl, launch); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateNodeGroupScheduling(ctx, id, "template-1", "template-2", tpl, launch); err != nil {
		t.Fatalf("exact retry: %v", err)
	}
	d, err := s.AutoscalerDiscovery(ctx, "cluster")
	if err != nil || len(d.Groups) != 1 || d.Groups[0].TemplateRevision != "template-2" || d.Groups[0].Scheduling.Zone != "us-west-2a" {
		t.Fatalf("refresh: %#v %v", d, err)
	}
	tpl.Zone = "wrong"
	tpl.Labels = map[string]string{"topology.kubernetes.io/zone": "us-west-2a"}
	if err := s.UpdateNodeGroupScheduling(ctx, id, "template-2", "template-3", tpl, launch); !errors.Is(err, ErrInvalid) {
		t.Fatalf("canonical override: %v", err)
	}
	tpl.Zone = "us-west-2a"
	if err := s.UpdateNodeGroupScheduling(ctx, id, "template-1", "template-2", tpl, launch); !errors.Is(err, ErrConflict) {
		t.Fatalf("same revision changed: %v", err)
	}
	tpl.Labels = nil
	if err := s.UpdateNodeGroupScheduling(ctx, id, "template-1", "template-3", tpl, launch); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale expected revision: %v", err)
	}
}

func TestTemplateUpdateRequiresAdmin(t *testing.T) {
	apiHandler := NewAPI(nil, "admin", "adapter", "cluster").Handler()
	for _, token := range []string{"", "Bearer adapter"} {
		r := httptest.NewRequest(http.MethodPatch, "/v1/node-groups/group/template", strings.NewReader(`{}`))
		r.Header.Set("Authorization", token)
		w := httptest.NewRecorder()
		apiHandler.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("token %q: %d", token, w.Code)
		}
	}
}

func TestBootstrapRetryEnrichesEmptySchedulingOnce(t *testing.T) {
	db := database(t)
	s := NewStore(db)
	g := api.NodeGroupInput{Naming: api.NodeGroupNaming{Provider: "aws", DevelopmentStage: "test", Region: "us-west-2", AZ: "a", Group: "general"}, ClusterID: "cluster", ZoneID: "zone", NodePoolID: "pool", CapacityClass: "general", ProviderZoneID: "us-west-2a", MaxNodes: 8, TemplateRevision: "template-1", PerNode: api.Resources{VCPU: 4, MemoryMiB: 8192}, LaunchParameters: []byte(`{"image_id":"ami-pinned","instance_type":"m7i.xlarge"}`), Scheduling: api.SchedulingTemplate{InstanceType: "m7i.xlarge", Architecture: "amd64", Region: "us-west-2", Zone: "us-west-2a"}}
	for i := 0; i < 2; i++ {
		if err := s.CreateNodeGroup(context.Background(), g); err != nil {
			t.Fatalf("retry %d: %v", i, err)
		}
	}
	g.Scheduling.Zone = "us-west-2b"
	if err := s.CreateNodeGroup(context.Background(), g); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed retry: %v", err)
	}
}
