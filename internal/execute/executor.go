package execute

import (
	"context"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
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
	ctx, span := otel.Tracer("crane.background").Start(ctx, "action.execute", trace.WithAttributes(attribute.String("host_id", action.HostID), attribute.String("action", string(action.Type))))
	defer func() { span.End() }()
	if action.Type == ActionDrainHost || action.Type == ActionReplaceHost {
		if err := e.catalog.EnsureLegacyLifecycle(ctx, action.HostID); err != nil {
			return err
		}
	}
	e.logger.InfoContext(ctx, "executing action", "operation", "action.execute", "host_id", action.HostID, "action", action.Type)
	switch action.Type {
	case ActionCreateHost:
		e.logger.InfoContext(ctx, "creating host", "operation", "action.execute", "host_id", action.HostID)

		// create new host, currently hardcoded to only use AWS, role is also being hardcoded
		hostID, err := e.catalog.CreateHost(ctx, "aws")
		if err != nil {
			e.logger.ErrorContext(ctx, "failed to create host", "operation", "action.execute", "error", err, "host_id", action.HostID)
			return err
		}

		// provision host here
		providerID, err := e.provider.ProvisionHost(ctx, "role", hostID)
		if err != nil {
			e.logger.ErrorContext(ctx, "failed to provision host", "operation", "action.execute", "error", err, "host_id", hostID)
			return err
		}

		// update host with providerID
		if err := e.catalog.UpdateHostProviderID(ctx, hostID, providerID); err != nil {
			e.logger.ErrorContext(ctx, "failed to update host provider id", "operation", "action.execute", "error", err, "host_id", hostID)
			return err
		}

	case ActionDrainHost:
		e.logger.InfoContext(ctx, "draining host", "operation", "action.execute", "host_id", action.HostID)

		// mark host draining in catalog
		return e.catalog.TransitionState(ctx, action.HostID, api.HostDraining)

	case ActionReplaceHost:
		e.logger.InfoContext(ctx, "replacing host", "operation", "action.execute", "host_id", action.HostID)

		// get providerid
		host, err := e.catalog.GetByID(ctx, action.HostID)
		if err != nil {
			e.logger.ErrorContext(ctx, "failed to get host", "operation", "action.execute", "error", err, "host_id", action.HostID)
			return err
		}

		// decommission host here
		if err := e.provider.TerminateHost(ctx, host.ProviderID); err != nil {
			e.logger.ErrorContext(ctx, "failed to terminate host", "operation", "action.execute", "error", err, "host_id", action.HostID)
			return err
		}

		if err := e.catalog.TransitionState(ctx, action.HostID, api.HostUnhealthy); err != nil {
			e.logger.ErrorContext(ctx, "failed to transition host state", "operation", "action.execute", "error", err, "host_id", action.HostID)
			return err
		}

	case ActionCreateLB:
		e.logger.InfoContext(ctx, "creating load balancer", "operation", "action.execute", "host_id", action.HostID)

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
			e.logger.ErrorContext(ctx, "failed to provision load balancer", "operation", "action.execute", "error", err)
			return err
		}

	case ActionCreateDB:
		e.logger.InfoContext(ctx, "creating database", "operation", "action.execute", "host_id", action.HostID)

		hostID, err := e.catalog.CreateHost(ctx, "aws")
		if err != nil {
			e.logger.ErrorContext(ctx, "failed to create host", "operation", "action.execute", "error", err, "host_id", action.HostID)
			return err
		}

		vpcID := "vpc-0fe5ab51bdf710362"
		subnetIDs := []string{"subnet-0809d7a67e415e07e"}

		connInfo, _, err := e.provider.ProvisionSpireDB(ctx, vpcID, subnetIDs, "sg-0a365ae1dce045677")
		if err != nil {
			e.logger.ErrorContext(ctx, "failed to provision spire database", "operation", "action.execute", "error", err, "host_id", hostID)
			return err
		}

		// Connection details are intentionally not logged: they are secrets.
		if err := e.catalog.UpdateDBConnectionInfo(ctx, hostID, connInfo); err != nil {
			e.logger.ErrorContext(ctx, "failed to store database connection info", "operation", "action.execute", "error", err, "host_id", hostID)
			return err
		}

		if err := e.catalog.UpdateState(ctx, hostID, string(api.HostReady)); err != nil {
			e.logger.ErrorContext(ctx, "failed to update host state", "operation", "action.execute", "error", err, "host_id", hostID)
			return err
		}

	case ActionCreateSpireInstance:
		e.logger.InfoContext(ctx, "creating spire instance", "operation", "action.execute", "host_id", action.HostID)

		// create new host, currently hardcoded to only use AWS, role is also being hardcoded
		hostID, err := e.catalog.CreateHost(ctx, "aws")
		if err != nil {
			e.logger.ErrorContext(ctx, "failed to create host", "operation", "action.execute", "error", err, "host_id", action.HostID)
			return err
		}

		vpcID := "vpc-0fe5ab51bdf710362"
		subnetIDs := []string{"subnet-0809d7a67e415e07e"}

		connInfo, _, err := e.provider.ProvisionSpireDB(ctx, vpcID, subnetIDs, "sg-0a365ae1dce045677")
		if err != nil {
			e.logger.ErrorContext(ctx, "failed to provision spire database", "operation", "action.execute", "error", err, "host_id", hostID)
			return err
		}

		// Connection details are intentionally not logged: they are secrets.
		if err := e.catalog.UpdateDBConnectionInfo(ctx, hostID, connInfo); err != nil {
			e.logger.ErrorContext(ctx, "failed to store database connection info", "operation", "action.execute", "error", err, "host_id", hostID)
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
			e.logger.ErrorContext(ctx, "failed to provision load balancer", "operation", "action.execute", "error", err, "host_id", hostID)
			return err
		}
		e.logger.InfoContext(ctx, "load balancer provisioned", "operation", "action.execute", "host_id", hostID, "dns", lbDNS)

		// provision host here
		providerID, err := e.provider.ProvisionSpireHost(ctx, hostID, connInfo)
		if err != nil {
			e.logger.ErrorContext(ctx, "failed to provision spire host", "operation", "action.execute", "error", err, "host_id", hostID)
			return err
		}

		// register instance with load balancer target group
		if err := e.provider.RegisterTargets(ctx, "spire-server-test-db-tg", providerID, 30000); err != nil {
			e.logger.ErrorContext(ctx, "failed to register targets", "operation", "action.execute", "error", err, "host_id", hostID)
			return err
		}

		// update host with providerID
		if err := e.catalog.UpdateHostProviderID(ctx, hostID, providerID); err != nil {
			e.logger.ErrorContext(ctx, "failed to update host provider id", "operation", "action.execute", "error", err, "host_id", hostID)
			return err
		}
	}

	return nil
}
