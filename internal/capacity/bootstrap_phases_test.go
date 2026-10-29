package capacity

import (
	"github.com/nabutabu/crane-oss/pkg/api"
	"testing"
)

func TestBootstrapPhaseSequences(t *testing.T) {
	for _, tc := range []struct {
		kind string
		want []api.ZoneBootstrapPhase
	}{
		{"create_cluster", []api.ZoneBootstrapPhase{api.ZoneRequested, api.ZoneDatabaseProvisioning, api.ZoneDatabaseReady, api.ZoneSpireHostProvisioning, api.ZoneSpireInstalling, api.ZoneSpireValidating, api.ZoneControlPlaneInitializing, api.ZoneNetworkInstalling, api.ZoneDominatorInstalling, api.ZoneValidating, api.ZoneReady}},
		{"attach_zone", []api.ZoneBootstrapPhase{api.ZoneRequested, api.ZoneInfrastructureProvisioning, api.ZoneNetworkInstalling, api.ZoneDominatorInstalling, api.ZoneValidating, api.ZoneReady}},
	} {
		for i := 0; i+1 < len(tc.want); i++ {
			got, ok := bootstrapNext(tc.kind, tc.want[i])
			if !ok || got != tc.want[i+1] {
				t.Fatalf("%s %s: got %s, %v", tc.kind, tc.want[i], got, ok)
			}
		}
		if _, ok := bootstrapNext(tc.kind, api.ZoneReady); ok {
			t.Fatalf("%s advanced past Ready", tc.kind)
		}
	}
}
