package execute

import (
	"context"

	"github.com/nabutabu/crane-oss/internal/hostcatalog/service"
	"github.com/nabutabu/crane-oss/internal/provider"
	"github.com/nabutabu/crane-oss/pkg/api"
)

type DefaultExecutor struct {
	catalog  *service.HostCatalogService
	provider provider.Provider
}

func NewDefaultExecutor(catalog *service.HostCatalogService, provider provider.Provider) *DefaultExecutor {
	return &DefaultExecutor{
		catalog:  catalog,
		provider: provider,
	}
}

type Executor interface {
	Execute(ctx context.Context, action *Action) error
}

func (e *DefaultExecutor) Execute(ctx context.Context, action *Action) error {
	switch action.Type {
	case ActionDrainHost:
		err := e.catalog.TransitionState(ctx, action.HostID, string(api.HostDraining))
		if err != nil {
			return err
		}
		//e.provider.ProvisionHost(ctx, )
	case ActionReplaceHost:
		err := e.catalog.TransitionState(ctx, action.HostID, string(api.HostUnhealthy))
		if err != nil {
			return err
		}
	}
	return nil
}
