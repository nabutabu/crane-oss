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
	Name string
}

type Fleet struct {
	name string
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
	Health     string
	CreatedAt  time.Time
}
