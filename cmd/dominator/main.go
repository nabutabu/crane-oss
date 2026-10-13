package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
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
	"github.com/nabutabu/crane-oss/internal/logging"
	"github.com/nabutabu/crane-oss/internal/telemetry"
	"github.com/nabutabu/crane-oss/internal/version"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
)

func main() {
	if err := run(); err != nil {
		slog.Error("dominator stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	logger := logging.New("dominator")
	logger.SetDefault()
	logger.Info("dominator", "version", version.String())
	host := config.GetEnv("DB_HOST", "localhost")
	port := config.GetEnvInt("DB_PORT", 43544)
	user := config.GetEnv("DB_USER", "postgres")
	password := config.GetEnv("DB_PASSWORD", "REDACTED_DB_PASSWORD")
	dbname := config.GetEnv("DB_NAME", "crane")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	telemetryProvider, err := telemetry.New(ctx, telemetry.Config{
		ServiceName:    "dominator",
		ServiceVersion: version.Version,
		Environment:    config.GetEnv("ENVIRONMENT", ""),
	})
	if err != nil {
		logger.Warn("telemetry disabled; continuing with no-op providers", "error", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := telemetryProvider.Shutdown(shutdownCtx); err != nil {
			logger.Error("telemetry shutdown failed", "error", err)
		}
	}()

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

	// 2. Create the connection string
	// The sslmode parameter is often set to 'disable' for local development.
	psqlInfo := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable",
		host, port, user, password, dbname)
	db, err := sql.Open("pgx", psqlInfo)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	if err := capacity.Migrate(ctx, db); err != nil {
		return fmt.Errorf("migrate capacity schema: %w", err)
	}
	hostStore := store.NewPostgresHostStore(db)

	hostCatalog := service.NewHostCatalogService(hostStore)

	resolver, err := dominator.NewPolicyResolver("./policy/os.star", logger.Logger)
	if err != nil {
		return fmt.Errorf("create policy resolver: %w", err)
	}

	server := dominator.NewServer(":44000", db, hostCatalog, resolver, logger.Logger)

	return server.StartContext(ctx, source)
}
