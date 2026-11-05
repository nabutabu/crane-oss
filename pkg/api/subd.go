package api

// ExpectedHostState changes only explicitly named resources; omitted resources
// are untouched. Revision is monotonically increasing within one host.
type ExpectedHostState struct {
	Revision uint64              `json:"revision"`
	ImageID  string              `json:"image_id,omitempty"`
	Track    string              `json:"track,omitempty"`
	Version  string              `json:"version,omitempty"`
	Services map[string]bool     `json:"services,omitempty"`
	Packages []HostPackageChange `json:"packages,omitempty"`
	Secret   *KubernetesSecret   `json:"secret,omitempty"`
}

type ClusterJoinRequest struct {
	Registration      WorkerRegistration `json:"registration"`
	Fence             int64              `json:"fence"`
	OperationID       string             `json:"operation_id"`
	APIServerEndpoint string             `json:"api_server_endpoint"`
	Token             string             `json:"token"`
	CACertHash        string             `json:"ca_cert_hash"`
}

type ClusterJoinResult struct {
	OperationID string `json:"operation_id"`
	Outcome     string `json:"outcome"`
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
	ExpectedVersion  string            `json:"expected_version,omitempty"`
	Kubernetes       *KubernetesResult `json:"kubernetes,omitempty"`
}

type HostAgentState struct {
	HostAgentStatus
	Current CurrentState `json:"current"`
}
