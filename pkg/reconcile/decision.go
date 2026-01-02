package reconcile

import (
	"github.com/nabutabu/crane-oss/internal/execute"
	"github.com/nabutabu/crane-oss/pkg/api"
)

type ReconcileDecision string

const (
	DecisionNone    ReconcileDecision = "none"
	DecisionDrain   ReconcileDecision = "drain"
	DecisionReplace ReconcileDecision = "replace"
)

func Decide(host *api.Host) *execute.Action {
	// for a given host decide what to do given host.Health and host.Status
	if host.Health == api.HostHealthHealthy && (host.State == api.HostReady || host.State == api.HostDraining) {
		return &execute.Action{
			HostID: host.ID,
			Type:   execute.ActionDrainHost,
		}
	} else if host.Health == api.HostHealthUnhealthy && (host.State == api.HostReady || host.State == api.HostDraining) {
		return &execute.Action{
			HostID: host.ID,
			Type:   execute.ActionReplaceHost,
		}
	}

	return &execute.Action{
		HostID: host.ID,
		Type:   execute.ActionDrainHost,
	}
}
