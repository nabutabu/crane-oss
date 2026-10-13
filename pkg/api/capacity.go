package api

import (
	"encoding/json"
	"time"
)

// Resources measures simultaneous capacity entitlement, never instance-hours.
// Memory is measured in MiB; VCPU and Nodes are integer quantities.
type Resources struct {
	VCPU      int64 `json:"vcpu"`
	MemoryMiB int64 `json:"memory_mib"`
	Nodes     int64 `json:"nodes"`
}

type Zone struct {
	ID, Provider, AccountID, Region, ProviderZoneID string
	Enabled                                         bool
	LifecycleState                                  string
}

// Cluster holds discovery configuration, never a plaintext bootstrap token.
type Cluster struct {
	ID, ZoneID, APIServerEndpoint, CACertHash                             string
	KubeadmVersion, KubeletVersion, ContainerRuntime, CNI, LifecycleState string
	JoinConfiguration                                                     json.RawMessage
}

type NodePool struct {
	ID, TeamID string
	Enabled    bool
	Limits     Resources
}

type NodeGroup struct {
	ID, ClusterID, NodePoolID, ZoneID, CapacityClass, ProviderZoneID string
	TargetNodes, TargetRevision, MinNodes, MaxNodes                  int64
	TemplateRevision, LifecycleState                                 string
	PerNode                                                          Resources
	// LaunchParameters is an immutable, version-pinned provider template.
	LaunchParameters json.RawMessage
}

// ClusterID must come from the authenticated adapter identity at the caller.
// Team identity and launch parameters are resolved from durable group policy.
type CapacityRequest struct {
	NodeGroupID, RequestID, ClusterID     string
	ExpectedTargetRevision, DesiredTarget int64
	TemplateRevision                      string
}

type CapacityResult struct {
	RequestID, NodeGroupID      string
	TargetNodes, TargetRevision int64
}

type CreditReservation struct {
	ID, TeamID, NodeGroupID, CapacityRequestID, HostID string
	Resources                                          Resources
	State                                              string
}

type Operation struct {
	ID, Kind, ZoneID, NodeGroupID, HostID, CapacityRequestID string
	TargetRevision                                           int64
	State, ProviderToken                                     string
	Parameters                                               json.RawMessage
	LeaseOwner                                               string
	LeaseUntil                                               time.Time
	Fence                                                    int64
	LastError                                                string
	CreatedAt, UpdatedAt                                     time.Time
}

type ZoneBootstrapPhase string

const (
	ZoneRequested                  ZoneBootstrapPhase = "Requested"
	ZoneInfrastructureProvisioning ZoneBootstrapPhase = "InfrastructureProvisioning"
	ZoneControlPlaneInitializing   ZoneBootstrapPhase = "ControlPlaneInitializing"
	ZoneNetworkInstalling          ZoneBootstrapPhase = "NetworkInstalling"
	ZoneDominatorInstalling        ZoneBootstrapPhase = "DominatorInstalling"
	ZoneValidating                 ZoneBootstrapPhase = "Validating"
	ZoneReady                      ZoneBootstrapPhase = "Ready"
	ZoneFailed                     ZoneBootstrapPhase = "Failed"
)

type ZoneBootstrapOperation struct {
	Operation
	Phase ZoneBootstrapPhase
}

type NodeRemovalOperation struct {
	Operation
	NodeName, ProviderID, Reason           string
	PreserveDesiredCapacity                bool
	DrainDeadline                          time.Time
	DrainStatus, ProviderTerminationStatus string
}
