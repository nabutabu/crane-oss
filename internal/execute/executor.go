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
	case ActionCreateHost:
		log.Println("action create host")

		// create new host, currently hardcoded to only use AWS, role is also being hardcoded
		hostID, err := e.catalog.CreateHost(ctx, "aws")
		if err != nil {
			log.Printf("err %v", err)
			return err
		}

		// provision host here
		provider_id, err := e.provider.ProvisionHost(ctx, "role", hostID)
		if err != nil {
			log.Printf("couldn't provision err %v", err)
			return err
		}

		// update host with providerID
		err = e.catalog.UpdateHostProviderID(ctx, hostID, provider_id)
		if err != nil {
			return err
		}

	case ActionDrainHost:
		log.Println("action drain host")

		// mark host draining in catalog
		return e.catalog.TransitionState(ctx, action.HostID, api.HostDraining)

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

		err = e.catalog.TransitionState(ctx, action.HostID, api.HostUnhealthy)
		if err != nil {
			return err
		}

	case ActionCreateLB:
		log.Println("action create Load Balancer")

		vpcID := "vpc-0fe5ab51bdf710362"
		subnetIDs := []string{"subnet-0f73ec402ea914eb5"}

		_, err := e.provider.ProvisionLB(ctx, vpcID, subnetIDs, api.LBConfig{
			Name:                    "spire-server-test-db",
			Port:                    8081,
			Internal:                true,
			Purpose:                 "Spire server",
			DeregistrationDelaySecs: 30,
		})

		if err != nil {
			log.Fatal(err)
		}

	case ActionCreateDB:
		log.Println("action create DB")

		vpcID := "vpc-0fe5ab51bdf710362"
		subnetIDs := []string{"subnet-0809d7a67e415e07e"}

		connInfo, _, err := e.provider.ProvisionSpireDB(ctx, vpcID, subnetIDs, "sg-0a365ae1dce045677")
		if err != nil {
			log.Fatal(err)
		}

		log.Printf("[Executor] connection info for new DB:\n %v\n", connInfo)
	}

	return nil
}
