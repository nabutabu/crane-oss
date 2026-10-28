// crane-bootstrap creates a protected platform through a provider, or seeds a local database.
package main

import (
	"bytes"
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
	"github.com/nabutabu/crane-oss/internal/provider/registry"
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
	raw := flags.String("config", "", "first-zone JSON (optional override for platform bootstrap)")
	configPath := flags.String("bootstrap-config", "", "versioned platform configuration JSON")
	stateDir := flags.String("state-dir", "", "local deployment state directory (defaults beside bootstrap config)")
	prepareImages := flags.Bool("prepare-images", false, "prepare the private identity endpoint before building host images")
	dryRun := flags.Bool("dry-run", false, "validate local configuration and print phases without provider calls")
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
		var header struct {
			Version int `json:"version"`
		}
		if err := json.Unmarshal(data, &header); err != nil {
			return fmt.Errorf("decode bootstrap configuration: %w", err)
		}
		if header.Version != 3 {
			return fmt.Errorf("unsupported bootstrap config version %d; use version 3", header.Version)
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&cfg); err != nil {
			return fmt.Errorf("decode bootstrap configuration: %w", err)
		}
		cfg = cfg.WithDefaults()
	}
	var in api.FirstZoneInput
	if *raw != "" {
		data, err := os.ReadFile(*raw)
		if err != nil {
			return err
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&in); err != nil {
			return fmt.Errorf("decode first zone configuration: %w", err)
		}
		if decoder.Decode(&struct{}{}) != io.EOF {
			return fmt.Errorf("first zone configuration must contain one JSON object")
		}
		cfg.FirstZone = data
	}
	if *dsn == "" && *configPath == "" {
		return fmt.Errorf("provide -bootstrap-config for platform provisioning, or -dsn and -config for database-only initialization")
	}
	if *configPath != "" {
		if err := cfg.Validate(); err != nil {
			return err
		}
	}
	reporter := bootstrap.NewReporter(out, *jsonOutput)
	if *dsn == "" {
		if *pending {
			return fmt.Errorf("-pending is only used with an explicit database DSN")
		}

		factory, err := registry.Bootstrap(cfg.Provider)
		if err != nil {
			return err
		}
		if _, err := factory(cfg); err != nil {
			return err
		}
		if *dryRun {
			return reporter.Planned(bootstrap.Phases(*prepareImages))
		}
		if *stateDir == "" {
			*stateDir = filepath.Join(filepath.Dir(*configPath), ".crane-bootstrap", cfg.Name)
		}
		result, err := bootstrap.Run(ctx, cfg, factory, reporter, bootstrap.RunOptions{StateDir: *stateDir, PrepareImages: *prepareImages})
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

		_, err = fmt.Fprintf(out, "platform %s: %s; provider=%s; identity=%s; state=%s\n", result.Name, status, result.Provider, result.IdentityEndpoint, *stateDir)
		if err == nil && result.ControlPlaneEndpoint != "" {
			_, err = fmt.Fprintf(out, "Crane API: %s; target host: %s\n", result.ControlPlaneEndpoint, result.TargetHostID)
			if err == nil {
				if result.ObservabilityStatus == "configuration_pending" {
					_, err = fmt.Fprintln(out, "Observability configuration pending")
				} else {
					_, err = fmt.Fprintf(out, "Observability: %s\n", result.ObservabilityStatus)
				}
			}
			if err == nil {
				_, err = fmt.Fprintln(out, result.ConnectionInstructions)
			}
		}
		return err
	}
	if *prepareImages {
		return fmt.Errorf("-prepare-images requires platform provisioning without a database DSN")
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
	return bootstrap.InitializeDatabase(ctx, db, in, *pending, reporter)
}
