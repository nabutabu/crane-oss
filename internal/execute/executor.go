package execute

import (
	"context"
	"log"

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
	log.Printf("/Execute hostID: %s type: %s\n", action.HostID, action.Type)
	switch action.Type {
	case ActionDrainHost:
		log.Println("action drain host")
		// provision host here
		provider_id, err := e.provider.ProvisionHost(ctx, action.HostID)
		if err != nil {
			return err
		}

		// err = e.catalog.TransitionState(ctx, action.HostID, string(api.HostDraining))
		// if err != nil {
		// 	return err
		// }

		// create new host
		err = e.catalog.CreateHost(ctx, provider_id, "aws")
		if err != nil {
			return err
		}

	case ActionReplaceHost:
		log.Println("action replace host")
		// get providerid
		host, err := e.catalog.GetByID(ctx, action.HostID)
		if err != nil {
			return err
		}

		// decommission host here
		err = e.provider.TerminateHost(ctx, host.ProviderID)
		if err != nil {
			return err
		}

		err = e.catalog.TransitionState(ctx, action.HostID, string(api.HostUnhealthy))
		if err != nil {
			return err
		}

	}

	return nil
}
