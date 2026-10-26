package bootstrap

import (
	"context"
	"encoding/json"
)

// Factory validates provider configuration locally. Construction must not use
// credentials, execute provider commands, or mutate deployment state.
type Factory func(Config) (Session, error)

type HostPhase string

const (
	InstallIdentity   HostPhase = "identity-server"
	RegisterWorkloads HostPhase = "workload-identities"
	InstallPlatform   HostPhase = "platform"
	ValidateHandoff   HostPhase = "validate"
)

// Session owns provider-specific working state for one deployment invocation.
// Resolve returns canonical, secret-free configuration suitable for pinning.
// Its previous argument is the provider portion of the pinned configuration;
// Resolve must reject changes to pinned choices before any provisioning starts.
type Session interface {
	Resolve(context.Context, json.RawMessage, bool) (json.RawMessage, error)
	PrepareEndpoint(context.Context) error
	EnsureCredentials(context.Context) error
	EnsureInfrastructure(context.Context) error
	EnsureHosts(context.Context) error
	RunHostPhase(context.Context, HostPhase, *Reporter) error
	Result() Result
}

type Result struct {
	Name                   string          `json:"name"`
	Provider               string          `json:"provider"`
	IdentityEndpoint       string          `json:"identity_endpoint"`
	APIServerEndpoint      string          `json:"api_server_endpoint,omitempty"`
	ControlPlaneEndpoint   string          `json:"control_plane_endpoint,omitempty"`
	TargetHostID           string          `json:"target_host_id,omitempty"`
	ObservabilityStatus    string          `json:"observability_status,omitempty"`
	ConnectionInstructions string          `json:"connection_instructions,omitempty"`
	ProviderOutput         json.RawMessage `json:"provider_output,omitempty"`
}
