package capacity

import "github.com/nabutabu/crane-oss/pkg/api"

// bootstrapNext is the durable phase graph for each layer-zero operation.
// A zone attachment joins an existing cluster, so it has no cluster or SPIRE
// initialization phases.
func bootstrapNext(kind string, phase api.ZoneBootstrapPhase) (api.ZoneBootstrapPhase, bool) {
	var sequence []api.ZoneBootstrapPhase
	switch kind {
	case "create_zone":
		sequence = []api.ZoneBootstrapPhase{api.ZoneRequested, api.ZoneDatabaseProvisioning, api.ZoneDatabaseReady, api.ZoneSpireHostProvisioning, api.ZoneSpireInstalling, api.ZoneSpireValidating, api.ZoneControlPlaneInitializing, api.ZoneNetworkInstalling, api.ZoneDominatorInstalling, api.ZoneValidating, api.ZoneReady}
	case "create_cluster":
		sequence = []api.ZoneBootstrapPhase{api.ZoneRequested, api.ZoneInfrastructureProvisioning, api.ZoneControlPlaneInitializing, api.ZoneNetworkInstalling, api.ZoneDominatorInstalling, api.ZoneValidating, api.ZoneReady}
	case "attach_zone":
		sequence = []api.ZoneBootstrapPhase{api.ZoneRequested, api.ZoneInfrastructureProvisioning, api.ZoneNetworkInstalling, api.ZoneDominatorInstalling, api.ZoneValidating, api.ZoneReady}
	default:
		return "", false
	}
	for i := 0; i+1 < len(sequence); i++ {
		if sequence[i] == phase {
			return sequence[i+1], true
		}
	}
	return "", false
}
