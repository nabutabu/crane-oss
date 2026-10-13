package execute

import (
	"context"
	"log/slog"

	"github.com/nabutabu/crane-oss/internal/hostcatalog/service"
	"github.com/nabutabu/crane-oss/internal/provider"
	"github.com/nabutabu/crane-oss/pkg/api"
)

type DefaultExecutor struct {
	catalog  *service.HostCatalogService
	provider provider.Provider
	logger   *slog.Logger
}

func NewDefaultExecutor(catalog *service.HostCatalogService, provider provider.Provider, loggers ...*slog.Logger) *DefaultExecutor {
	logger := slog.Default()
	if len(loggers) > 0 && loggers[0] != nil {
		logger = loggers[0]
	}
	return &DefaultExecutor{
		catalog:  catalog,
		provider: provider,
		logger:   logger,
	}
}

type Executor interface {
	Execute(ctx context.Context, action *Action) error
}

func (e *DefaultExecutor) Execute(ctx context.Context, action *Action) error {
	if action.Type == ActionDrainHost || action.Type == ActionReplaceHost {
		if err := e.catalog.EnsureLegacyLifecycle(ctx, action.HostID); err != nil {
			return err
		}
	}
	e.logger.Info("executing action", "host_id", action.HostID, "action", action.Type)
	switch action.Type {
	case ActionCreateHost:
		e.logger.Info("creating host", "host_id", action.HostID)

		// create new host, currently hardcoded to only use AWS, role is also being hardcoded
		hostID, err := e.catalog.CreateHost(ctx, "aws")
		if err != nil {
			e.logger.Error("failed to create host", "error", err, "host_id", action.HostID)
			return err
		}

		// provision host here
		providerID, err := e.provider.ProvisionHost(ctx, "role", hostID)
		if err != nil {
			e.logger.Error("failed to provision host", "error", err, "host_id", hostID)
			return err
		}

		// update host with providerID
		if err := e.catalog.UpdateHostProviderID(ctx, hostID, providerID); err != nil {
			e.logger.Error("failed to update host provider id", "error", err, "host_id", hostID)
			return err
		}

	case ActionDrainHost:
		e.logger.Info("draining host", "host_id", action.HostID)

		// mark host draining in catalog
		return e.catalog.TransitionState(ctx, action.HostID, api.HostDraining)

	case ActionReplaceHost:
		e.logger.Info("replacing host", "host_id", action.HostID)

		// get providerid
		host, err := e.catalog.GetByID(ctx, action.HostID)
		if err != nil {
			e.logger.Error("failed to get host", "error", err, "host_id", action.HostID)
			return err
		}

		// decommission host here
		if err := e.provider.TerminateHost(ctx, host.ProviderID); err != nil {
			e.logger.Error("failed to terminate host", "error", err, "host_id", action.HostID)
			return err
		}

		if err := e.catalog.TransitionState(ctx, action.HostID, api.HostUnhealthy); err != nil {
			e.logger.Error("failed to transition host state", "error", err, "host_id", action.HostID)
			return err
		}

	case ActionCreateLB:
		e.logger.Info("creating load balancer", "host_id", action.HostID)

		vpcID := "vpc-0fe5ab51bdf710362"
		subnetIDs := []string{"subnet-0f73ec402ea914eb5"}

		_, err := e.provider.ProvisionLB(ctx, vpcID, subnetIDs, api.LBConfig{
			Name:                    "spire-server-test-db",
			Port:                    30000,
			Internal:                true,
			Purpose:                 "Spire server",
			DeregistrationDelaySecs: 30,
		})
		if err != nil {
			e.logger.Error("failed to provision load balancer", "error", err)
			return err
		}

	case ActionCreateDB:
		e.logger.Info("creating database", "host_id", action.HostID)

		hostID, err := e.catalog.CreateHost(ctx, "aws")
		if err != nil {
			e.logger.Error("failed to create host", "error", err, "host_id", action.HostID)
			return err
		}

		vpcID := "vpc-0fe5ab51bdf710362"
		subnetIDs := []string{"subnet-0809d7a67e415e07e"}

		connInfo, _, err := e.provider.ProvisionSpireDB(ctx, vpcID, subnetIDs, "sg-0a365ae1dce045677")
		if err != nil {
			e.logger.Error("failed to provision spire database", "error", err, "host_id", hostID)
			return err
		}

		// Connection details are intentionally not logged: they are secrets.
		if err := e.catalog.UpdateDBConnectionInfo(ctx, hostID, connInfo); err != nil {
			e.logger.Error("failed to store database connection info", "error", err, "host_id", hostID)
			return err
		}

		if err := e.catalog.UpdateState(ctx, hostID, string(api.HostReady)); err != nil {
			e.logger.Error("failed to update host state", "error", err, "host_id", hostID)
			return err
		}

	case ActionCreateSpireInstance:
		e.logger.Info("creating spire instance", "host_id", action.HostID)

		// create new host, currently hardcoded to only use AWS, role is also being hardcoded
		hostID, err := e.catalog.CreateHost(ctx, "aws")
		if err != nil {
			e.logger.Error("failed to create host", "error", err, "host_id", action.HostID)
			return err
		}

		vpcID := "vpc-0fe5ab51bdf710362"
		subnetIDs := []string{"subnet-0809d7a67e415e07e"}

		connInfo, _, err := e.provider.ProvisionSpireDB(ctx, vpcID, subnetIDs, "sg-0a365ae1dce045677")
		if err != nil {
			e.logger.Error("failed to provision spire database", "error", err, "host_id", hostID)
			return err
		}

		// Connection details are intentionally not logged: they are secrets.
		if err := e.catalog.UpdateDBConnectionInfo(ctx, hostID, connInfo); err != nil {
			e.logger.Error("failed to store database connection info", "error", err, "host_id", hostID)
			return err
		}

		vpcID = "vpc-0fe5ab51bdf710362"
		subnetIDs = []string{"subnet-0f73ec402ea914eb5"}

		lbDNS, err := e.provider.ProvisionLB(ctx, vpcID, subnetIDs, api.LBConfig{
			Name:                    "spire-server-test-db",
			Port:                    30000,
			Internal:                true,
			Purpose:                 "Spire server",
			DeregistrationDelaySecs: 30,
		})
		if err != nil {
			e.logger.Error("failed to provision load balancer", "error", err, "host_id", hostID)
			return err
		}
		e.logger.Info("load balancer provisioned", "host_id", hostID, "dns", lbDNS)

		// provision host here
		providerID, err := e.provider.ProvisionSpireHost(ctx, hostID, connInfo)
		if err != nil {
			e.logger.Error("failed to provision spire host", "error", err, "host_id", hostID)
			return err
		}

		// register instance with load balancer target group
		if err := e.provider.RegisterTargets(ctx, "spire-server-test-db-tg", providerID, 30000); err != nil {
			e.logger.Error("failed to register targets", "error", err, "host_id", hostID)
			return err
		}

		// update host with providerID
		if err := e.catalog.UpdateHostProviderID(ctx, hostID, providerID); err != nil {
			e.logger.Error("failed to update host provider id", "error", err, "host_id", hostID)
			return err
		}
	}

	return nil
}
