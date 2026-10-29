package capacity

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nabutabu/crane-oss/pkg/api"
)

type bootstrapWorkerFake struct{ phases []api.ZoneBootstrapPhase }

func (f *bootstrapWorkerFake) Step(_ context.Context, _ api.Operation, phase api.ZoneBootstrapPhase) error {
	f.phases = append(f.phases, phase)
	return nil
}
func (f *bootstrapWorkerFake) Validated(context.Context, api.Operation) (bool, error) {
	return true, nil
}

func TestPostgresBootstrapWorkerAdvancesAndRestarts(t *testing.T) {
	db := database(t)
	s := NewStore(db)
	ctx := context.Background()
	zone := api.ZoneInput{ID: "worker-zone", Provider: "aws", AccountID: "account", Region: "us-west-2", ProviderZoneID: "us-west-2b"}
	group := testGroup(zone, "worker")
	if err := group.NormalizeID(); err != nil {
		t.Fatal(err)
	}
	params, err := json.Marshal(map[string]any{"zone": zone, "membership": api.ClusterZoneInput{ClusterID: "cluster", ZoneID: zone.ID}, "pool": api.NodePoolInput{ID: "pool", Limits: api.Resources{VCPU: 32, MemoryMiB: 65536, Nodes: 8}}, "group": group})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateBootstrap(ctx, api.BootstrapRequest{Kind: "attach_zone", RequestID: "worker-request", ClusterID: "cluster", ZoneID: zone.ID, Parameters: params}); err != nil {
		t.Fatal(err)
	}
	fake := &bootstrapWorkerFake{}
	for range 5 {
		controller := BootstrapController{Store: s, Executor: fake, Lease: time.Minute}
		if err := controller.RunOnce(ctx, zone.ID, "worker"); err != nil {
			t.Fatal(err)
		}
	}
	if len(fake.phases) != 4 || fake.phases[0] != api.ZoneInfrastructureProvisioning || fake.phases[3] != api.ZoneValidating {
		t.Fatalf("phases %v", fake.phases)
	}
	if _, err := s.NextBootstrapZone(ctx); err == nil {
		t.Fatal("completed operation remains runnable")
	}
}
