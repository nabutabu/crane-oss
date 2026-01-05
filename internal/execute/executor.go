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
		err := e.catalog.TransitionState(ctx, action.HostID, string(api.HostDraining))
		if err != nil {
			return err
		}
		// provision host here

	case ActionReplaceHost:
		log.Println("action replace host")
		err := e.catalog.TransitionState(ctx, action.HostID, string(api.HostUnhealthy))
		if err != nil {
			return err
		}

		// decommission host here
	}

	log.Println("returning nil")
	return nil
}
