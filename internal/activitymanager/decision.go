package activitymanager

import (
	"github.com/nabutabu/crane-oss/internal/badhost/problem"
	"github.com/nabutabu/crane-oss/internal/execute"
)

func Decide(host_id string, problems []problem.Problem) *execute.Action {
	if len(problems) == 0 {
		return nil
	}

	// Count problems by severity and type
	criticalCount := 0
	hasSmartFailure := false
	hasUnreachable := false
	hasCycling := false

	for _, p := range problems {
		switch p.Severity {
		case problem.SeverityCritical:
			criticalCount++
		}

		switch p.Type {
		case problem.ProblemTypeSMART:
			hasSmartFailure = true
		case problem.ProblemTypeReachability:
			hasUnreachable = true
		case problem.ProblemTypeCycling:
			hasCycling = true
		}
	}

	// Decision logic based on problems
	if hasSmartFailure || hasCycling {
		// Smart failure or cycling host requires replacement
		return &execute.Action{
			HostID: host_id,
			Type:   execute.ActionReplaceHost,
		}
	} else if hasUnreachable {
		// Unreachable host should be drained first
		return &execute.Action{
			HostID: host_id,
			Type:   execute.ActionDrainHost,
		}
	} else if criticalCount >= 2 {
		// Multiple critical problems warrant replacement
		return &execute.Action{
			HostID: host_id,
			Type:   execute.ActionReplaceHost,
		}
	} else if criticalCount >= 1 {
		// Single critical problem - try draining first
		return &execute.Action{
			HostID: host_id,
			Type:   execute.ActionDrainHost,
		}
	}

	// No action needed for warning/info level problems
	return nil
}
