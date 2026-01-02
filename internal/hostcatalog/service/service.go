package service

import (
	"context"
	"errors"
	"slices"

	"github.com/nabutabu/crane-oss/internal/hostcatalog/store"
	"github.com/nabutabu/crane-oss/pkg/api"
)

type HostCatalogService struct {
	store store.PostgresHostStore
}

func NewHostCatalogService(store store.PostgresHostStore) *HostCatalogService {
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
	newState string,
) error {
	// 1. load host
	host, err := service.store.GetByID(ctx, id)
	if err != nil {
		return errors.New("Host not found")
	}

	// convert newState to api.HostState
	state := api.HostState(newState)

	// 2. validate transition
	validNextStates := GetValidNextStates(host.State)
	if !slices.Contains(validNextStates, state) {
		return errors.New("Not a valid next state")
	}

	// 3. update new state
	return service.store.UpdateState(ctx, id, state)
}

func (service *HostCatalogService) TransitionHealth(ctx context.Context, id string, newHealth string) error {
	// convert newState to api.HostState
	health := api.HostHealth(newHealth)

	return service.store.UpdateHealth(ctx, id, health)
}
