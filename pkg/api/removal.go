package api

// NodeRemovalRequest admits an entire named-node deletion as one durable intent.
// Cluster identity is supplied by the authenticated transport, never this body.
type NodeRemovalRequest struct {
	NodeGroupID            string        `json:"node_group_id"`
	RequestID              string        `json:"request_id"`
	ExpectedTargetRevision int64         `json:"expected_target_revision"`
	Nodes                  []RemovalNode `json:"nodes"`
}

type RemovalNode struct {
	HostID     string `json:"host_id"`
	NodeName   string `json:"node_name"`
	ProviderID string `json:"provider_id"`
}
