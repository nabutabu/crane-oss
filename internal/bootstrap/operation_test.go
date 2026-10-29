package bootstrap

import (
	"context"
	"testing"

	"github.com/nabutabu/crane-oss/pkg/api"
)

type operationSessionFake struct{ calls []string }

func (f *operationSessionFake) EnsureInfrastructure(context.Context, api.Operation) error {
	f.calls = append(f.calls, "infrastructure")
	return nil
}
func (f *operationSessionFake) InitializeControlPlane(context.Context, api.Operation) error {
	f.calls = append(f.calls, "control-plane")
	return nil
}
func (f *operationSessionFake) EnsureNetwork(context.Context, api.Operation) error {
	f.calls = append(f.calls, "network")
	return nil
}
func (f *operationSessionFake) EnsureDominator(context.Context, api.Operation) error {
	f.calls = append(f.calls, "dominator")
	return nil
}
func (f *operationSessionFake) Validate(context.Context, api.Operation) (bool, error) {
	f.calls = append(f.calls, "validate")
	return true, nil
}

func TestOperationExecutorDispatch(t *testing.T) {
	for _, tc := range []struct {
		kind   string
		phases []api.ZoneBootstrapPhase
		want   []string
	}{
		{"create_cluster", []api.ZoneBootstrapPhase{api.ZoneInfrastructureProvisioning, api.ZoneControlPlaneInitializing, api.ZoneNetworkInstalling, api.ZoneDominatorInstalling}, []string{"infrastructure", "control-plane", "network", "dominator", "validate"}},
		{"attach_zone", []api.ZoneBootstrapPhase{api.ZoneInfrastructureProvisioning, api.ZoneNetworkInstalling, api.ZoneDominatorInstalling}, []string{"infrastructure", "network", "dominator", "validate"}},
	} {
		fake := &operationSessionFake{}
		exec := OperationExecutor{Session: fake}
		op := api.Operation{Kind: tc.kind}
		for _, phase := range tc.phases {
			if err := exec.Step(context.Background(), op, phase); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := exec.Validated(context.Background(), op); err != nil {
			t.Fatal(err)
		}
		if len(fake.calls) != len(tc.want) {
			t.Fatalf("%s: %v", tc.kind, fake.calls)
		}
		for i := range tc.want {
			if fake.calls[i] != tc.want[i] {
				t.Fatalf("%s: %v", tc.kind, fake.calls)
			}
		}
	}
}
