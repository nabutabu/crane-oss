package api

// ExpectedHostState changes only explicitly named resources; omitted resources
// are untouched. Revision is monotonically increasing within one host.
type ExpectedHostState struct {
	Revision uint64              `json:"revision"`
	Services map[string]bool     `json:"services,omitempty"`
	Packages []HostPackageChange `json:"packages,omitempty"`
	Secret   *KubernetesSecret   `json:"secret,omitempty"`
}

type HostPackageChange struct {
	Name   string `json:"name"`
	Action string `json:"action"` // install or remove
}

type HostAgentStatus struct {
	Identity         string            `json:"identity"`
	PID              int               `json:"pid"`
	InvocationID     string            `json:"invocation_id"`
	Ready            bool              `json:"ready"`
	AcceptedRevision uint64            `json:"accepted_revision"`
	AppliedRevision  uint64            `json:"applied_revision"`
	Outcome          string            `json:"outcome"`
	Kubernetes       *KubernetesResult `json:"kubernetes,omitempty"`
}

type HostAgentState struct {
	HostAgentStatus
	Current CurrentState `json:"current"`
}
