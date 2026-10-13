package service

import (
	"slices"
	"testing"

	"github.com/nabutabu/crane-oss/pkg/api"
)

func TestReadinessRequiresRegistrationAndConvergence(t *testing.T) {
	for _, state := range []api.HostState{api.HostRequested, api.HostProvisioning, api.HostBooting, api.HostRegistering} {
		if slices.Contains(GetValidNextStates(state), api.HostReady) {
			t.Errorf("%s can skip readiness gates", state)
		}
	}
	if !slices.Contains(GetValidNextStates(api.HostConverging), api.HostReady) {
		t.Fatal("converged host cannot become ready")
	}
	if slices.Contains(GetValidNextStates(api.HostDraining), api.HostTerminated) {
		t.Fatal("drain completion skips provider termination")
	}
	if !slices.Contains(GetValidNextStates(api.HostTerminating), api.HostTerminated) {
		t.Fatal("provider termination cannot complete")
	}
}
