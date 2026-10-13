package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/nabutabu/crane-oss/internal/config"
	"github.com/nabutabu/crane-oss/internal/dominator"
	"github.com/nabutabu/crane-oss/internal/hostcatalog/service"
	"github.com/nabutabu/crane-oss/internal/hostcatalog/store"
	"github.com/nabutabu/crane-oss/internal/logging"
	"github.com/nabutabu/crane-oss/internal/version"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
)

func main() {
	logger := logging.New("dominator")
	logger.Info("dominator", "version", version.String())
	host := config.GetEnv("DB_HOST", "localhost")
	port := config.GetEnvInt("DB_PORT", 43544)
	user := config.GetEnv("DB_USER", "postgres")
	password := config.GetEnv("DB_PASSWORD", "REDACTED_DB_PASSWORD")
	dbname := config.GetEnv("DB_NAME", "crane")

	ctx := context.Background()

	source, err := workloadapi.NewX509Source(ctx)
	if err != nil {
		logger.Error("failed to create X509 source", "error", err)
		os.Exit(1)
	}
	defer source.Close()

	svid, err := source.GetX509SVID()
	if err != nil {
		logger.Error("Couldnt get X509", "error", err)
		os.Exit(1)
	}

	logger.Info("SPIFFE ID", "spiffe_id", svid.ID.String())

	// 2. Create the connection string
	// The sslmode parameter is often set to 'disable' for local development.
	psqlInfo := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable",
		host, port, user, password, dbname)
	db, err := sql.Open("pgx", psqlInfo)
	if err != nil {
		logger.Error("error", "err", err)
	}
	hostStore := store.NewPostgresHostStore(db)

	hostCatalog := service.NewHostCatalogService(hostStore)

	resolver, err := dominator.NewPolicyResolver("./policy/os.star")
	if err != nil {
		logger.Error("error", "err", err)
		os.Exit(1)
	}

	server := dominator.NewServer(":44000", db, hostCatalog, resolver)

	if err := server.Start(source); err != nil {
		logger.Error("error", "err", err)
		os.Exit(1)
	}
}
