// crane-bootstrap creates the protected AWS platform, or seeds a local database.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/nabutabu/crane-oss/internal/bootstrap"
	"github.com/nabutabu/crane-oss/internal/capacity"
	"github.com/nabutabu/crane-oss/pkg/api"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "bootstrap:", err)
		os.Exit(1)
	}
}

func run(parent context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("crane-bootstrap", flag.ContinueOnError)
	dsn := flags.String("dsn", os.Getenv("CRANE_DATABASE_URL"), "PostgreSQL DSN for explicit database-only initialization")
	raw := flags.String("config", "", "first-zone JSON (optional override for AWS bootstrap)")
	configPath := flags.String("bootstrap-config", "", "versioned AWS platform configuration JSON")
	region := flags.String("aws-region", "", "override bootstrap AWS region")
	profile := flags.String("aws-profile", "", "AWS CLI profile (defaults to the AWS credential chain)")
	stateDir := flags.String("state-dir", "", "local deployment state directory (defaults beside bootstrap config)")
	prepareImages := flags.Bool("prepare-images", false, "create the private SPIRE endpoint before building golden AMIs")
	dryRun := flags.Bool("dry-run", false, "validate local configuration and print phases without AWS calls")
	jsonOutput := flags.Bool("json", false, "emit JSON events only on stdout")
	pending := flags.Bool("pending", false, "seed first zone in initializing state until validated")
	timeout := flags.Duration("operation-timeout", 90*time.Minute, "maximum duration of this invocation")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *timeout <= 0 {
		return fmt.Errorf("operation-timeout must be positive")
	}
	ctx, cancel := context.WithTimeout(parent, *timeout)
	defer cancel()
	var cfg bootstrap.Config
	if *configPath != "" {
		data, err := os.ReadFile(*configPath)
		if err != nil {
			return err
		}
		if err = json.Unmarshal(data, &cfg); err != nil {
			return fmt.Errorf("decode bootstrap configuration: %w", err)
		}
		if *region != "" {
			cfg.AWS.Region = *region
		}
		cfg = cfg.WithDefaults()
	}
	var in api.FirstZoneInput
	if *raw != "" {
		data, err := os.ReadFile(*raw)
		if err != nil {
			return err
		}
		if err = json.Unmarshal(data, &in); err != nil {
			return fmt.Errorf("decode first zone configuration: %w", err)
		}
		cfg.FirstZone = data
	}
	if *dsn == "" && *configPath == "" {
		return fmt.Errorf("provide -bootstrap-config for AWS provisioning, or -dsn and -config for database-only initialization")
	}
	if *configPath != "" {
		if err := cfg.Validate(); err != nil {
			return err
		}
	}
	reporter := bootstrap.NewReporter(out, *jsonOutput)
	if *dsn == "" {
		if cfg.Version != 2 {
			return fmt.Errorf("AWS platform provisioning requires configuration version 2")
		}
		if *pending {
			return fmt.Errorf("-pending is only used with an explicit database DSN")
		}
		if *dryRun {
			return reporter.Planned(bootstrap.AWSPhases(*prepareImages))
		}
		if *stateDir == "" {
			*stateDir = filepath.Join(filepath.Dir(*configPath), ".crane-bootstrap", cfg.Name)
		}
		result, err := bootstrap.RunAWS(ctx, cfg, bootstrap.CLI{Region: cfg.AWS.Region, Profile: *profile}, reporter, bootstrap.RunOptions{StateDir: *stateDir, PrepareImages: *prepareImages})
		if err != nil {
			return err
		}
		status := "ready"
		if *prepareImages {
			status = "endpoint-prepared"
		}
		if *jsonOutput {
			return json.NewEncoder(out).Encode(struct {
				Status string           `json:"status"`
				Result bootstrap.Result `json:"result"`
			}{status, result})
		}
		if *prepareImages {
			_, err = fmt.Fprintf(out, "private SPIRE hostname: %s; bundle parameter: %s; state: %s\n", result.SpireAddress, result.BundleParameter, *stateDir)
			return err
		}
		_, err = fmt.Fprintf(out, "platform %s is ready in %s; SPIRE=%s, platform=%s, credentials secret=%s\n", result.Name, result.Region, result.SpireInstanceID, result.PlatformInstanceID, result.RuntimeSecretARN)
		return err
	}
	if *prepareImages {
		return fmt.Errorf("-prepare-images requires AWS provisioning without a database DSN")
	}
	if *raw == "" {
		if len(cfg.FirstZone) == 0 {
			return fmt.Errorf("database-only initialization requires -config or first_zone in the bootstrap config")
		}
		if err := json.Unmarshal(cfg.FirstZone, &in); err != nil {
			return err
		}
	}
	if *dryRun {
		return reporter.Planned([]string{"initialize Crane schema", "seed first zone"})
	}
	db, err := sql.Open("pgx", *dsn)
	if err != nil {
		return fmt.Errorf("open Crane database: %w", err)
	}
	defer db.Close()
	if err = reporter.Run(2, "initialize Crane schema", func() (string, error) {
		if err := reporter.Detail("database", "connecting and applying repeatable Crane schema"); err != nil {
			return "", err
		}
		return "", capacity.InitializeSchema(ctx, db)
	}); err != nil {
		return err
	}
	return reporter.Run(2, "seed first zone", func() (string, error) {
		if err := reporter.Detail("topology", fmt.Sprintf("seeding zone %s with target_nodes=0", in.Zone.ID)); err != nil {
			return "", err
		}
		s := capacity.NewStore(db)
		var err error
		if *pending {
			err = s.SeedFirstZonePending(ctx, in.Zone, in.Cluster, in.Budget, in.Pool, in.Group)
		} else {
			err = s.SeedFirstZone(ctx, in.Zone, in.Cluster, in.Budget, in.Pool, in.Group)
		}
		return "zone " + in.Zone.ID + ", target_nodes=0", err
	})
}
