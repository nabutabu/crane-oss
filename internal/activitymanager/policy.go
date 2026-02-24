package activitymanager

import (
	"time"

	"github.com/nabutabu/crane-oss/internal/badhost/problem"
)

type EscalationLevel int

const (
	EscalationNone EscalationLevel = iota
	EscalationDrain
	EscalationReplace
)

func EvaluateEscalation(
	problems []problem.Problem,
	now time.Time,
) EscalationLevel {
	// Count problems in different time windows
	var last10min, last30min int
	tenMinAgo := now.Add(-10 * time.Minute)
	thirtyMinAgo := now.Add(-30 * time.Minute)

	for _, problem := range problems {
		if problem.DetectedAt.After(tenMinAgo) {
			last10min++
		}
		if problem.DetectedAt.After(thirtyMinAgo) {
			last30min++
		}
	}

	// Apply escalation policy
	// if last30min >= 3 {
	// 	return EscalationReplace
	// } else if last10min >= 1 {
	// 	return EscalationDrain
	// } else {
	// 	return EscalationNone
	// }

	return EscalationReplace
}
