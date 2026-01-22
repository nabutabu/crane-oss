package activitymanager

import (
	"time"

	"github.com/nabutabu/crane-oss/internal/badhost/problem"
	"github.com/nabutabu/crane-oss/internal/execute"
)

func Decide(host_id string, problems []problem.Problem) *execute.Action {
	if len(problems) == 0 {
		return nil
	}

	// Evaluate escalation level based on recent problems
	escalation := EvaluateEscalation(problems, time.Now())

	// Map escalation level to action type
	switch escalation {
	case EscalationReplace:
		return &execute.Action{
			HostID: host_id,
			Type:   execute.ActionReplaceHost,
		}
	case EscalationDrain:
		return &execute.Action{
			HostID: host_id,
			Type:   execute.ActionDrainHost,
		}
	case EscalationNone:
		return nil
	default:
		return nil
	}
}
