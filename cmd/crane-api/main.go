package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/nabutabu/crane-oss/internal/activitymanager"
	"github.com/nabutabu/crane-oss/internal/activitymanager/problemcache"
	"github.com/nabutabu/crane-oss/internal/badhost"
	"github.com/nabutabu/crane-oss/internal/badhost/checks"
	"github.com/nabutabu/crane-oss/internal/badhost/problem"
	"github.com/nabutabu/crane-oss/internal/capacity"
	intconfig "github.com/nabutabu/crane-oss/internal/config"
	"github.com/nabutabu/crane-oss/internal/execute"
	"github.com/nabutabu/crane-oss/internal/hostcatalog/service"
	"github.com/nabutabu/crane-oss/internal/hostcatalog/store"
	"github.com/nabutabu/crane-oss/internal/logging"
	"github.com/nabutabu/crane-oss/internal/provider/awscompute"
	"github.com/nabutabu/crane-oss/internal/telemetry"
	"github.com/nabutabu/crane-oss/internal/version"
	"go.opentelemetry.io/otel"
)

const BHD_CONFIG_YAML_PATH = "bhd.json"

type configJSON struct {
	Zone                string   `json:"zone"`
	ScanIntervalMinutes int      `json:"scan_interval"`
	Checks              []string `json:"checks"`
}

func LoadConfigFromFile(path string) (*badhost.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var tmp configJSON
	err = json.Unmarshal(data, &tmp)
	if err != nil {
		return nil, err
	}

	return &badhost.Config{
		Zone:         tmp.Zone,
		ScanInterval: time.Duration(tmp.ScanIntervalMinutes) * time.Minute,
		Checks:       tmp.Checks,
	}, nil
}

func BuildChecks(enabled []string, deps checks.Dependencies) ([]checks.Check, error) {
	var res []checks.Check

	for _, name := range enabled {
		factory, ok := checks.CheckCatalog[name]
		if !ok {
			return nil, fmt.Errorf("check does not exist: %s", name)
		}

		res = append(res, factory(deps))
	}

	return res, nil
}

// healthHandler serves the liveness and database-readiness probes.
type healthHandler struct {
	logger *slog.Logger
	db     *sql.DB
}

func (h *healthHandler) health(w http.ResponseWriter, r *http.Request) {
	h.logger.InfoContext(r.Context(), "healthy", "path", r.URL.Path)
	fmt.Fprintf(w, `{"status": "available"}`)
}

func (h *healthHandler) dbHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if err := h.db.PingContext(r.Context()); err != nil {
		h.logger.ErrorContext(r.Context(), "database health check failed", "error", err)
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"status": "unavailable"}`)
		return
	}
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"status": "available"}`)
}

func main() {
	if err := run(); err != nil {
		os.Exit(1)
	}
}

func run() (runErr error) {
	// Exporter diagnostics retain a local-only, redacting handler.
	localLogger := logging.NewWithOTLP("crane-api", nil)
	localLogger.SetDefault()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	telemetryProvider, err := telemetry.New(ctx, telemetry.Config{
		ServiceName:    "crane-api",
		ServiceVersion: version.Version,
		Environment:    intconfig.GetEnv("ENVIRONMENT", ""),
		EnableLogs:     true,
		ErrorHandler: otel.ErrorHandlerFunc(func(error) {
			localLogger.Warn("telemetry export failed; check collector delivery health")
		}),
	})
	logger := logging.NewWithOTLP("crane-api", telemetryProvider.LoggerProvider)
	logger.SetDefault()
	if err != nil {
		logger.Warn("telemetry disabled; continuing with no-op providers", "error", err)
	}
	defer func() {
		if runErr != nil {
			logger.ErrorContext(ctx, "crane-api stopped", "error", runErr)
		} else {
			logger.InfoContext(ctx, "crane-api stopped")
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := telemetryProvider.Shutdown(shutdownCtx); err != nil {
			localLogger.Error("telemetry shutdown failed")
		}
	}()

	logger.InfoContext(ctx, "crane-api starting", "version", version.String())

	db, err := sql.Open("pgx", intconfig.DatabaseURL(5432))
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	if err := capacity.CheckSchema(ctx, db); err != nil {
		return fmt.Errorf("Crane database is not ready: %w", err)
	}

	actionStore := execute.NewPostgresActionStore(db)
	hostStore := store.NewPostgresHostStore(db)

	hostCatalog := service.NewHostCatalogService(hostStore)

	cfg, err := config.LoadDefaultConfig(context.TODO(),
		config.WithRegion(intconfig.GetEnv("AWS_REGION", "us-west-2")),
	)
	if err != nil {
		return fmt.Errorf("load SDK config: %w", err)
	}

	ec2Client := ec2.NewFromConfig(cfg)

	deps := checks.Dependencies{
		EC2Client:   ec2Client,
		HostCatalog: hostCatalog,
	}

	provider := awscompute.New(ec2Client)

	executor := execute.NewDefaultExecutor(
		hostCatalog,
		provider,
		logger.Logger,
	)

	worker := execute.NewWorker(
		actionStore,
		executor,
	)

	problemStore := problem.New(db)

	bhd_config, err := LoadConfigFromFile(BHD_CONFIG_YAML_PATH)
	if err != nil {
		return fmt.Errorf("load bad host detector config: %w", err)
	}

	checks, err := BuildChecks(bhd_config.Checks, deps)
	if err != nil {
		return fmt.Errorf("build checks: %w", err)
	}

	bhd := badhost.New(hostCatalog, problemStore, checks, bhd_config)

	manager := activitymanager.NewActivityManager(problemStore, actionStore, problemcache.NewCache(10, time.Hour), time.Minute*5, time.Minute)

	// Activity Manager - Create actions based on problems seen by BHD
	go manager.Run(ctx)

	// Bad Host Detector - Detect problems with existing Hosts in a Zone
	go bhd.Run(ctx)

	// Worker to put the hosts in appropriate states once detected by BHD
	go worker.Run(ctx)

	health := &healthHandler{logger: logger.Logger, db: db}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", health.health)
	mux.HandleFunc("/v1/db/health", health.dbHealth)

	// Platform topology and capacity admission endpoints. The topology routes
	// require CRANE_ADMIN_TOKEN; adapter credentials are bound to one cluster,
	// and a caller cannot choose its own team identity.
	capacityAPI := capacity.NewAPI(capacity.NewStore(db), intconfig.GetEnv("CRANE_ADMIN_TOKEN", ""), intconfig.GetEnv("CRANE_ADAPTER_TOKEN", ""), intconfig.GetEnv("CRANE_ADAPTER_CLUSTER_ID", ""))
	mux.Handle("/v1/", capacityAPI.Handler())

	server := &http.Server{Addr: ":43060", Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("HTTP shutdown failed", "error", err)
		}
	}()
	err = server.ListenAndServe()
	stop()
	<-shutdownDone
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
