package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/nabutabu/crane-oss/internal/capacity"
	"github.com/nabutabu/crane-oss/internal/config"
	"github.com/nabutabu/crane-oss/internal/dominator"
	"github.com/nabutabu/crane-oss/internal/hostcatalog/service"
	"github.com/nabutabu/crane-oss/internal/hostcatalog/store"
	"github.com/nabutabu/crane-oss/internal/hostrpc"
	"github.com/nabutabu/crane-oss/internal/logging"
	"github.com/nabutabu/crane-oss/internal/provider/awscompute"
	"github.com/nabutabu/crane-oss/internal/telemetry"
	"github.com/nabutabu/crane-oss/internal/version"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
	"go.opentelemetry.io/otel"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func main() {
	if err := run(); err != nil {
		os.Exit(1)
	}
}

func run() (runErr error) {
	// Exporter diagnostics must remain local to avoid recursively exporting
	// failures from the logging pipeline itself.
	localLogger := logging.NewWithOTLP("dominator", nil)
	localLogger.SetDefault()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	telemetryProvider, err := telemetry.New(ctx, telemetry.Config{
		ServiceName:    "dominator",
		ServiceVersion: version.Version,
		Environment:    config.GetEnv("ENVIRONMENT", ""),
		EnableLogs:     true,
		ErrorHandler: otel.ErrorHandlerFunc(func(error) {
			localLogger.Warn("telemetry export failed; check collector delivery health")
		}),
	})
	logger := logging.NewWithOTLP("dominator", telemetryProvider.LoggerProvider)
	logger.SetDefault()
	if err != nil {
		logger.Warn("telemetry disabled; continuing with no-op providers", "error", err)
	}
	defer func() {
		if runErr != nil {
			logger.ErrorContext(ctx, "dominator stopped", "error", runErr)
		} else {
			logger.InfoContext(ctx, "dominator stopped")
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := telemetryProvider.Shutdown(shutdownCtx); err != nil {
			localLogger.Error("telemetry shutdown failed")
		}
	}()
	logger.InfoContext(ctx, "dominator starting", "version", version.String())

	source, err := workloadapi.NewX509Source(ctx)
	if err != nil {
		return fmt.Errorf("create X509 source: %w", err)
	}
	defer source.Close()

	svid, err := source.GetX509SVID()
	if err != nil {
		return fmt.Errorf("get X509 SVID: %w", err)
	}

	logger.Info("SPIFFE ID", "spiffe_id", svid.ID.String())

	db, err := sql.Open("pgx", config.DatabaseURL(43544))
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	if err := capacity.CheckSchema(ctx, db); err != nil {
		return fmt.Errorf("Crane database is not ready: %w", err)
	}
	hostStore := store.NewPostgresHostStore(db)

	hostCatalog := service.NewHostCatalogService(hostStore)

	resolver, err := dominator.NewPolicyResolver("./policy/os.star", logger.Logger)
	if err != nil {
		return fmt.Errorf("create policy resolver: %w", err)
	}
	trustDomain := config.GetEnv("SPIFFE_TRUST_DOMAIN", "crane-oss")
	zoneID := config.GetEnv("CRANE_ZONE_ID", "")
	awsRuntime, err := awscompute.NewRuntime(ctx, config.GetEnv("AWS_REGION", "us-west-2"), trustDomain)
	if err != nil {
		return fmt.Errorf("create AWS runtime provisioner: %w", err)
	}
	kubeConfig, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("create Dominator Kubernetes client configuration: %w", err)
	}
	kubernetesClient, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		return fmt.Errorf("create Dominator Kubernetes client: %w", err)
	}
	clusterID := config.GetEnv("CRANE_CLUSTER_ID", "")
	if clusterID == "" {
		return fmt.Errorf("CRANE_CLUSTER_ID is required")
	}
	clusterCA := kubeConfig.CAData
	if len(clusterCA) == 0 {
		clusterCA, err = os.ReadFile(kubeConfig.CAFile)
		if err != nil {
			return fmt.Errorf("cannot load Kubernetes cluster trust root")
		}
	}
	provisioner := &dominator.ProvisionExecutor{Store: capacity.NewStore(db), Provider: awsRuntime,
		Kubernetes: kubernetesClient, Caller: hostrpc.Client{Source: source}, Resolver: resolver, TrustDomain: trustDomain, ClusterID: clusterID, ClusterCA: clusterCA}
	provisionController := &capacity.ProvisionController{Store: capacity.NewStore(db), Executor: provisioner,
		ZoneID: zoneID, Identity: svid.ID.String()}
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		_ = provisionController.Run(ctx, 5*time.Second, logger.Logger)
	}()
	defer func() { stop(); <-workerDone }()

	server := dominator.NewServer(":44000", db, hostCatalog, resolver, logger.Logger)
	server.Middleware = func(next http.Handler) http.Handler { return telemetryProvider.HTTPHandler(next, logger.Logger) }

	return server.StartContext(ctx, source)
}
