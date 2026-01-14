package badhost

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

type Condition struct {
	Field    string
	Values   []string
	Operator string
	Value    int
}

type ProblemDefinition struct {
	Name        string
	Description string
	Severity    ProblemSeverity
	Conditions  []Condition
}

type Problem struct {
	ID         int
	Host_id    string
	Type       ProblemType
	Severity   ProblemSeverity
	DetectedAt time.Time
	ResolvedAt time.Time
	Details    string
}

// Centralized hardware problem definitions
var HardwareProblemDefinitions = map[ProblemType]ProblemDefinition{
	ProblemTypeSEL: {
		Name:        "SEL Event",
		Description: "Security Enhanced Linux event detected",
		Severity:    SeverityWarning,
		Conditions: []Condition{
			{Field: "sel_type", Values: []string{"AVC", "DENIED"}},
			{Field: "sel_type", Values: []string{"MAC_POLICY_LOAD"}},
		},
	},
	ProblemTypeSMART: {
		Name:        "SMART Failure",
		Description: "S.M.A.R.T. disk health monitoring failure",
		Severity:    SeverityCritical,
		Conditions: []Condition{
			{Field: "smart_status", Values: []string{"failing"}},
			{Field: "reallocated_sectors", Operator: ">", Value: 0},
			{Field: "pending_sectors", Operator: ">", Value: 0},
		},
	},
	// Add more problem definitions...
}
