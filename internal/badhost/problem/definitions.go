package problem

import "time"

type ProblemType string
type ProblemSeverity string

const (
	ProblemTypeSEL          ProblemType = "sel_event"
	ProblemTypeSMART        ProblemType = "smart_failure"
	ProblemTypeFirmware     ProblemType = "outdated_firmware"
	ProblemTypeCloudEvent   ProblemType = "cloud_health_event"
	ProblemTypeReachability ProblemType = "unreachable"
	ProblemTypeCycling      ProblemType = "cycling_host"
)
const (
	SeverityCritical ProblemSeverity = "critical"
	SeverityWarning  ProblemSeverity = "warning"
	SeverityInfo     ProblemSeverity = "info"
)

type Problem struct {
	ID         int
	Host_id    string
	Type       ProblemType
	Severity   ProblemSeverity
	DetectedAt time.Time
	ResolvedAt time.Time
	Details    string
}