package api

import (
	"time"
)

type HostState string

const (
	HostProvisioning HostState = "PROVISIONING"
	HostReady        HostState = "READY"
	HostDraining     HostState = "DRAINING"
	HostTerminated   HostState = "TERMINATED"
	HostUnhealthy    HostState = "UNHEALTHY"
)

type CPU string

const (
	Core_16 CPU = "16"
)

type Memory string

const (
	GB_8 Memory = "8"
)

type Capacity struct {
	cpu    CPU
	memory Memory
}

type Role struct {
	Name          string
	ExpectedImage string
}

type Fleet struct {
	name string
}

type HostHealth string

const (
	HostHealthUnknown   HostHealth = "unknown"
	HostHealthHealthy   HostHealth = "healthy"
	HostHealthUnhealthy HostHealth = "unhealthy"
)

type HealthRequest struct {
	Health string
}

type Host struct {
	ID                string
	HostName          string
	ProviderID        string
	Provider          string
	Role              Role
	Zone              string
	Fleet             Fleet
	ImageID           string
	Capacity          Capacity
	State             HostState
	Health            HostHealth
	CreatedAt         time.Time
	LastSeenHeartbeat time.Time
	Endpoint          string
	Port              int32
	DBName            string
	Username          string
	SecretARN         string
	RDSSGID           string
}

type DesiredState struct {
	ImageID  string `json:"image_id"`
	Track    string `json:"track"`
	Version  string `json:"version"`
	Services map[string]Service
	Packages []Package
}

type Service struct {
	Name        string
	Running     bool
	Description string
}

type Package struct {
	Name    string
	Version string
}

type CurrentState struct {
	Services map[string]Service
	Packages []Package
}

type ServiceAction struct {
	Name   string
	Action ServiceActionType
}

type PackageAction struct {
	Name   string
	Action PackageActionType
}

type ServiceActionType string

const (
	StartService ServiceActionType = "start"
	StopService  ServiceActionType = "stop"
)

type PackageActionType string

const (
	InstallPackage   PackageActionType = "install"
	UninstallPackage PackageActionType = "uninstall"
)

type LBConfig struct {
	// Name is used for both the NLB and target group names — must be unique
	// per service and ≤32 chars (AWS limit).
	Name string

	// Port the service listens on inside the EC2 instance (e.g. 8081 for
	// SPIRE, 9090 for Prometheus, etc.)
	Port int32

	// Internal keeps the NLB off the public internet. Set true for
	// service-to-service traffic, false for publicly reachable endpoints.
	Internal bool

	// Purpose is stamped onto AWS tags for cost allocation and filtering.
	Purpose string

	// DeregistrationDelaySecs controls how long the NLB drains connections
	// before removing a target. Default 30s; increase for long-lived streams.
	DeregistrationDelaySecs int32
}

// DBConnectionInfo is what callers need to build the SPIRE server config.
type DBConnectionInfo struct {
	Endpoint  string
	Port      int32
	DBName    string
	Username  string
	SecretARN string // retrieve password from here at runtime
	SGID      string // RDS security group — attach to any future peered VPCs
}
