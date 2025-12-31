package service

import (
	"context"

	"github.com/nabutabu/crane-oss/internal/hostcatalog/store"
	"github.com/nabutabu/crane-oss/pkg/api"
)

type HostCatalogService struct {
	store store.PostgresHostStore
}

func NewHostCatalogService(store store.PostgresHostStore) *HostCatalogService {
	return &HostCatalogService{store: store}
}

func (s *HostCatalogService) TransitionState(
	ctx context.Context,
	id string,
	newState api.HostState,
) error {
	// 1. load host

	// 2. validate transition
	// 3. persist new state
	return nil
}
