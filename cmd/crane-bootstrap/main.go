// crane-bootstrap seeds the platform team's first zone. It is deliberately a
// separate command so initialization is explicit and auditable; it never
// creates capacity operations.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/nabutabu/crane-oss/internal/bootstrap"
	"github.com/nabutabu/crane-oss/internal/capacity"
	"github.com/nabutabu/crane-oss/pkg/api"
)

func main() {
	dsn := flag.String("dsn", os.Getenv("CRANE_DATABASE_URL"), "PostgreSQL DSN")
	var raw string
	flag.StringVar(&raw, "config", "", "JSON file containing zone, cluster, budget, pool, and group")
	bootstrapConfig := flag.String("bootstrap-config", "", "versioned layer-zero AWS configuration JSON")
	region := flag.String("aws-region", "", "override the AWS region from bootstrap configuration")
	dryRun := flag.Bool("dry-run", false, "validate configuration without making changes")
	jsonOutput := flag.Bool("json", false, "emit machine-readable phase events")
	timeout := flag.Duration("operation-timeout", 30*time.Minute, "maximum duration for one bootstrap invocation")
	flag.Parse()

	if raw == "" {
		log.Fatal("usage: crane-bootstrap [-dsn <postgres-dsn>] -config <json-file>")
	}

	reporter := bootstrap.NewReporter(os.Stdout, *jsonOutput)
	phase := func(name string, fn func() (string, error)) {
		if err := reporter.Run(7, name, fn); err != nil {
			log.Fatal(err)
		}
	}
	skipped := func(detail string) func() (string, error) {
		return func() (string, error) { return detail, nil }
	}

	data, err := os.ReadFile(raw)
	if err != nil {
		log.Fatal(err)
	}

	var in api.FirstZoneInput
	if err = json.Unmarshal(data, &in); err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	var cfg bootstrap.Config
	if *bootstrapConfig != "" {
		configData, readErr := os.ReadFile(*bootstrapConfig)
		if readErr != nil {
			log.Fatal(readErr)
		}
		if readErr = json.Unmarshal(configData, &cfg); readErr != nil {
			log.Fatal(readErr)
		}
		if *region != "" {
			cfg.AWS.Region = *region
		}
		cfg = cfg.WithDefaults()
		if readErr = cfg.Validate(); readErr != nil {
			log.Fatal(readErr)
		}
	}
	phase("validate configuration", func() (string, error) {
		if *bootstrapConfig == "" {
			return "", nil
		}
		return "valid", cfg.Validate()
	})

	if *dryRun {
		fmt.Println("planned phases: validate configuration, ensure RDS, wait for RDS, ensure databases, initialize Crane schema, seed first zone")
		return
	}

	if strings.TrimSpace(*dsn) == "" {
		log.Fatal("-dsn is required until AWS RDS credential resolution is configured")
	}

	db, err := sql.Open("pgx", *dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	const skipExplicitDSN = "skipped (explicit -dsn)"
	phase("ensure RDS", skipped(skipExplicitDSN))
	phase("ensure RDS instance", skipped(skipExplicitDSN))
	phase("wait for RDS availability", skipped(skipExplicitDSN))
	phase("ensure databases", skipped(skipExplicitDSN))
	phase("initialize Crane schema", func() (string, error) {
		return "", capacity.InitializeSchema(ctx, db)
	})
	phase("seed first zone", func() (string, error) {
		detail := "zone " + in.Zone.ID + ", target_nodes=0"
		return detail, capacity.NewStore(db).SeedFirstZone(ctx, in.Zone, in.Cluster, in.Budget, in.Pool, in.Group)
	})

	fmt.Printf("first zone %s is ready; node group target remains zero\n", in.Zone.ID)
}
