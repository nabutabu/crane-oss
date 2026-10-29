package api

import (
	"encoding/json"
	"time"
)

// Resources measures simultaneous infrastructure capacity, never instance-hours.
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

// TopologyInput types are used by the authenticated platform API. They keep
// provisioning intent explicit and version-pinned at the admission boundary.
type ZoneInput struct {
	ID             string `json:"id"`
	Provider       string `json:"provider"`
	AccountID      string `json:"account_id"`
	Region         string `json:"region"`
	ProviderZoneID string `json:"provider_zone_id"`
}

type ClusterInput struct {
	ID                string `json:"id"`
	APIServerEndpoint string `json:"api_server_endpoint"`
	CACertHash        string `json:"ca_cert_hash"`
	KubeadmVersion    string `json:"kubeadm_version"`
	KubeletVersion    string `json:"kubelet_version"`
	ContainerRuntime  string `json:"container_runtime"`
	CNI               string `json:"cni"`
}

type NodePoolInput struct {
	ID     string    `json:"id"`
	Limits Resources `json:"limits"`
}

// ClusterZoneInput records operator-validated membership policy, independently
// of cluster control-plane and provider-zone readiness.
type ClusterZoneInput struct {
	ClusterID      string `json:"cluster_id"`
	ZoneID         string `json:"zone_id"`
	LifecycleState string `json:"lifecycle_state"`
	AccessState    string `json:"access_state"`
}

type ClusterZone = ClusterZoneInput

type NodeGroupNaming struct {
	Provider         string `json:"provider"`
	DevelopmentStage string `json:"development_stage"`
	Region           string `json:"region"`
	AZ               string `json:"az"`
	Group            string `json:"group"`
}

type NodeGroupInput struct {
	Naming           NodeGroupNaming    `json:"naming"`
	ID               string             `json:"id"`
	ClusterID        string             `json:"cluster_id"`
	NodePoolID       string             `json:"node_pool_id"`
	ZoneID           string             `json:"zone_id"`
	CapacityClass    string             `json:"capacity_class"`
	ProviderZoneID   string             `json:"provider_zone_id"`
	MinNodes         int64              `json:"min_nodes"`
	MaxNodes         int64              `json:"max_nodes"`
	TemplateRevision string             `json:"template_revision"`
	PerNode          Resources          `json:"per_node"`
	LaunchParameters json.RawMessage    `json:"launch_parameters"`
	Scheduling       SchedulingTemplate `json:"scheduling"`
}

// SchedulingTemplate is operator supplied Kubernetes scheduling metadata.
// It is independent of provider launch parameters and naming AZ values.
type SchedulingTemplate struct {
	InstanceType string            `json:"instance_type"`
	Architecture string            `json:"architecture"`
	Region       string            `json:"region"`
	Zone         string            `json:"zone"`
	Labels       map[string]string `json:"labels,omitempty"`
	Taints       []SchedulingTaint `json:"taints,omitempty"`
}

type SchedulingTaint struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Effect string `json:"effect"`
}

type AutoscalerNode struct {
	HostID     string `json:"host_id"`
	ProviderID string `json:"provider_id"`
	State      string `json:"state"`
}

type AutoscalerGroup struct {
	ID               string             `json:"id"`
	ClusterID        string             `json:"cluster_id"`
	ZoneID           string             `json:"zone_id"`
	Pool             string             `json:"pool"`
	TemplateRevision string             `json:"template_revision"`
	MinNodes         int64              `json:"min_nodes"`
	MaxNodes         int64              `json:"max_nodes"`
	CPU              int64              `json:"cpu"`
	MemoryMiB        int64              `json:"memory_mib"`
	LaunchReady      bool               `json:"launch_ready"`
	Scheduling       SchedulingTemplate `json:"scheduling"`
	Nodes            []AutoscalerNode   `json:"nodes"`
}

type AutoscalerDiscovery struct {
	Groups []AutoscalerGroup `json:"groups"`
}

// FirstZoneInput is the complete layer-zero topology seeded by both the
// crane-bootstrap command and the /v1/first-zone endpoint.
type FirstZoneInput struct {
	Zone       ZoneInput        `json:"zone"`
	Cluster    ClusterInput     `json:"cluster"`
	Membership ClusterZoneInput `json:"membership"`
	Pool       NodePoolInput    `json:"pool"`
	Group      NodeGroupInput   `json:"group"`
}

// Cluster holds discovery configuration, never a plaintext bootstrap token.
type Cluster struct {
	ID, APIServerEndpoint, CACertHash                                     string
	KubeadmVersion, KubeletVersion, ContainerRuntime, CNI, LifecycleState string
	JoinConfiguration                                                     json.RawMessage
}

type NodePool struct {
	ID      string
	Enabled bool
	Limits  Resources
}

type NodeGroup struct {
	Naming                                                           NodeGroupNaming
	ID, ClusterID, NodePoolID, ZoneID, CapacityClass, ProviderZoneID string
	TargetNodes, TargetRevision, MinNodes, MaxNodes                  int64
	TemplateRevision, LifecycleState                                 string
	PerNode                                                          Resources
	// LaunchParameters is an immutable, version-pinned provider template.
	LaunchParameters json.RawMessage
}

// ClusterID must come from the authenticated adapter identity at the caller.
// Launch parameters and shared pool limits are resolved from durable policy.
type CapacityRequest struct {
	NodeGroupID, RequestID, ClusterID     string
	ExpectedTargetRevision, DesiredTarget int64
	TemplateRevision                      string
}

type CapacityResult struct {
	RequestID, NodeGroupID      string
	TargetNodes, TargetRevision int64
}

type InfrastructureReservation struct {
	ID, NodeGroupID, CapacityRequestID, HostID string
	Resources                                  Resources
	State                                      string
}

type Operation struct {
	ID, Kind, ZoneID, NodeGroupID, HostID, CapacityRequestID string
	// Populated from the trusted catalog for bootstrap execution, never from
	// the operation's immutable request parameters.
	ClusterID, APIServerEndpoint, CACertHash string
	TargetRevision                           int64
	State, ProviderToken                     string
	Parameters                               json.RawMessage
	LeaseOwner                               string
	LeaseUntil                               time.Time
	Fence                                    int64
	LastError                                string
	CreatedAt, UpdatedAt                     time.Time
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

// BootstrapRequest is immutable operator intent. Parameters must contain only
// public configuration and opaque references, never join tokens or secrets.
type BootstrapRequest struct {
	Kind       string          `json:"kind"`
	RequestID  string          `json:"request_id"`
	ClusterID  string          `json:"cluster_id"`
	ZoneID     string          `json:"zone_id"`
	Parameters json.RawMessage `json:"parameters"`
}

// BootstrapStepResult contains public cluster discovery returned by a
// provider step. It must never carry join credentials or private key material.
type BootstrapStepResult struct {
	APIServerEndpoint string `json:"api_server_endpoint,omitempty"`
	CACertHash        string `json:"ca_cert_hash,omitempty"`
	ProviderHostID string `json:"provider_host_id,omitempty"`
	HostPrivateIP string `json:"host_private_ip,omitempty"`
	ImageID string `json:"image_id,omitempty"`
}

type NodeRemovalOperation struct {
	Operation
	NodeName, ProviderID, Reason           string
	PreserveDesiredCapacity                bool
	DrainDeadline                          time.Time
	DrainStatus, ProviderTerminationStatus string
}
