package api

// HostSummary exposes inventory identity, placement and status without the
// database connection fields carried by the internal Host model.
type HostSummary struct {
	ID                   string     `json:"id"`
	Provider             string     `json:"provider"`
	ProviderID           string     `json:"provider_id"`
	Region               string     `json:"region"`
	ZoneID               string     `json:"zone_id"`
	Role                 string     `json:"role"`
	State                HostState  `json:"state"`
	Health               HostHealth `json:"health"`
	Protected            bool       `json:"protected"`
	NodeGroupID          string     `json:"node_group_id,omitempty"`
	NodePoolID           string     `json:"node_pool_id,omitempty"`
	DesiredStateVersion  string     `json:"desired_state_version,omitempty"`
	ObservedStateVersion string     `json:"observed_state_version,omitempty"`
}

type HostList struct {
	Hosts []HostSummary `json:"hosts"`
}
