package service

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/nabutabu/crane-oss/internal/hostcatalog/store"
	"github.com/nabutabu/crane-oss/pkg/api"
)

type HostCatalogService struct {
	store *store.PostgresHostStore
}

var ErrManagedCapacity = errors.New("capacity-managed host requires a fenced durable operation")

// EnsureLegacyLifecycle keeps the legacy action queue from becoming a second
// lifecycle authority for hosts admitted through the capacity transaction.
func (service *HostCatalogService) EnsureLegacyLifecycle(ctx context.Context, id string) error {
	var managed bool
	if err := service.store.DB.QueryRowContext(ctx, `SELECT node_group_id IS NOT NULL FROM host WHERE id=$1`, id).Scan(&managed); err != nil {
		return err
	}
	if managed {
		return ErrManagedCapacity
	}
	return nil
}

func NewHostCatalogService(store *store.PostgresHostStore) *HostCatalogService {
	return &HostCatalogService{store: store}
}

func GetValidNextStates(currState api.HostState) []api.HostState {
	switch currState {
	case api.HostRequested:
		return []api.HostState{api.HostProvisioning, api.HostTerminated, api.HostFailed}
	case api.HostProvisioning:
		return []api.HostState{api.HostBooting, api.HostFailed}
	case api.HostBooting:
		return []api.HostState{api.HostRegistering, api.HostFailed}
	case api.HostRegistering:
		return []api.HostState{api.HostConverging, api.HostFailed}
	case api.HostConverging:
		return []api.HostState{api.HostReady, api.HostFailed}
	case api.HostReady:
		return []api.HostState{api.HostDraining, api.HostUnhealthy}
	case api.HostDraining:
		return []api.HostState{api.HostUnhealthy, api.HostTerminating}
	case api.HostTerminating:
		return []api.HostState{api.HostTerminated, api.HostFailed}
	case api.HostUnhealthy:
		return []api.HostState{api.HostReady, api.HostTerminated}
	case api.HostTerminated:
		return []api.HostState{}
	}

	return []api.HostState{}
}

func (service *HostCatalogService) TransitionState(
	ctx context.Context,
	id string,
	newState api.HostState,
) error {
	if err := service.EnsureLegacyLifecycle(ctx, id); err != nil {
		return err
	}
	slog.Info("transitioning host state", "host_id", id, "new_state", newState)
	// 1. load host
	host, err := service.store.GetByID(ctx, id)
	if err != nil {
		return errors.New("Host not found")
	}

	// 2. validate transition
	validNextStates := GetValidNextStates(host.State)
	if !slices.Contains(validNextStates, newState) {
		return errors.New("Not a valid next state")
	}

	// 3. update new state
	return service.store.UpdateState(ctx, id, newState)
}

func (service *HostCatalogService) TransitionHealth(ctx context.Context, id string, newHealth string) error {
	// convert newState to api.HostState
	health := api.HostHealth(newHealth)

	return service.store.UpdateHealth(ctx, id, health)
}

func (service *HostCatalogService) GetByID(ctx context.Context, id string) (*api.Host, error) {
	return service.store.GetByID(ctx, id)
}

func (service *HostCatalogService) GetByProviderID(ctx context.Context, id string) (*api.Host, error) {
	return service.store.GetByProviderID(ctx, id)
}

func (service *HostCatalogService) CreateHost(ctx context.Context, provider string) (string, error) {
	id := uuid.NewString()
	return id, service.store.Create(ctx, &api.Host{
		ID:         id,
		ProviderID: "",
		Provider:   provider,
		Zone:       "us-west-2",
		State:      api.HostProvisioning,
		CreatedAt:  time.Now(),
	})
}

func (service *HostCatalogService) UpdateHostProviderID(ctx context.Context, hostID string, providerID string) error {
	if err := service.EnsureLegacyLifecycle(ctx, hostID); err != nil {
		return err
	}
	return service.store.UpdateProviderID(ctx, hostID, providerID)
}

func (service *HostCatalogService) UpdateLastSeenHeartbeat(ctx context.Context, hostID string, lastSeen time.Time) error {
	return service.store.UpdateLastSeenHeartbeat(ctx, hostID, lastSeen)
}

func (service *HostCatalogService) DeleteHost(ctx context.Context, id string) error {
	if err := service.EnsureLegacyLifecycle(ctx, id); err != nil {
		return err
	}
	return service.store.Delete(ctx, id)
}

func (service *HostCatalogService) GetByZone(ctx context.Context, zone string) ([]*api.Host, error) {
	return service.store.GetByZone(ctx, zone)
}

func (service *HostCatalogService) GetByToken(ctx context.Context, token string) (*api.Host, error) {
	return service.store.GetByToken(ctx, token)
}

func (service *HostCatalogService) UpdateState(ctx context.Context, id string, newState string) error {
	if err := service.EnsureLegacyLifecycle(ctx, id); err != nil {
		return err
	}
	return service.store.UpdateState(ctx, id, api.HostState(newState))
}

func (service *HostCatalogService) UpdateDBConnectionInfo(ctx context.Context, hostID string, connInfo api.DBConnectionInfo) error {
	return service.store.UpdateDBConnectionInfo(ctx, hostID, connInfo.Endpoint, connInfo.Port, connInfo.DBName, connInfo.Username, connInfo.SecretARN, connInfo.SGID)
}
