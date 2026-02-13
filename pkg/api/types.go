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
	ID         string
	HostName   string
	ProviderID string
	Provider   string
	Role       Role
	Zone       string
	Fleet      Fleet
	ImageID    string
	Capacity   Capacity
	State      HostState
	Health     HostHealth
	CreatedAt  time.Time
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
