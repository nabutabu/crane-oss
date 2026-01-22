package service

import (
	"context"
	"errors"
	"log"
	"slices"

	"github.com/google/uuid"
	"github.com/nabutabu/crane-oss/internal/hostcatalog/store"
	"github.com/nabutabu/crane-oss/pkg/api"
)

type HostCatalogService struct {
	store *store.PostgresHostStore
}

func NewHostCatalogService(store *store.PostgresHostStore) *HostCatalogService {
	return &HostCatalogService{store: store}
}

func GetValidNextStates(currState api.HostState) []api.HostState {
	switch currState {
	case api.HostProvisioning:
		return []api.HostState{api.HostReady}
	case api.HostReady:
		return []api.HostState{api.HostDraining, api.HostUnhealthy}
	case api.HostDraining:
		return []api.HostState{api.HostUnhealthy, api.HostTerminated}
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
	log.Printf("/service.go/TransitionState %s newState: %s\n", id, newState)
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

func (service *HostCatalogService) CreateHost(ctx context.Context, provider string) (string, error) {
	id := uuid.NewString()
	return id, service.store.Create(ctx, &api.Host{
		ID:         id,
		ProviderID: "",
		Provider:   provider,
		Zone:       "us-west-2",
		State:      api.HostProvisioning,
	})
}

func (service *HostCatalogService) UpdateHostProviderID(ctx context.Context, hostID string, providerID string) error {
	return service.store.UpdateProviderID(ctx, hostID, providerID)
}

func (service *HostCatalogService) DeleteHost(ctx context.Context, id string) error {
	return service.store.Delete(ctx, id)
}

func (service *HostCatalogService) GetByZone(ctx context.Context, zone string) ([]*api.Host, error) {
	return service.store.GetByZone(ctx, zone)
}
