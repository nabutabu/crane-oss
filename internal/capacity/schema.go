package capacity

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"strings"
)

//go:embed schema.sql
var baselineSchema string

// InitializeSchema installs Crane's first-go PostgreSQL schema. It is safe to
// run repeatedly and is intended for the explicit crane-bootstrap command.
// It does not track versions or upgrade an existing schema.
func InitializeSchema(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin schema initialization: %w", err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(724163001)`); err != nil {
		return fmt.Errorf("lock schema initialization: %w", err)
	}
	if _, err = tx.ExecContext(ctx, baselineSchema); err != nil {
		return fmt.Errorf("initialize Crane schema: %w", err)
	}
	return tx.Commit()
}

// CheckSchema verifies that bootstrap installed the required Crane tables.
// Services call this at startup and never alter the database schema.
func CheckSchema(ctx context.Context, db *sql.DB) error {
	required := []string{
		"zone_dominators", "host_state_requests",
		"zones", "clusters", "cluster_zones", "node_pools", "node_groups",
		"capacity_requests", "host", "operations", "infrastructure_reservations",
		"zone_bootstrap_operations", "node_removal_operations", "actions",
		"host_problems", "host_health_trends",
	}
	var missing []string
	for _, name := range required {
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, name).Scan(&exists); err != nil {
			return fmt.Errorf("check Crane schema readiness: %w", err)
		}
		if !exists {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		return fmt.Errorf("Crane schema is not initialized (missing tables: %s); run crane-bootstrap", strings.Join(missing, ", "))
	}
	var columns int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='host' AND column_name IN ('protected','subd_endpoint')`).Scan(&columns); err != nil {
		return fmt.Errorf("check host schema readiness: %w", err)
	}
	if columns != 2 {
		return fmt.Errorf("Crane host schema is incompatible; initialize a fresh database with crane-bootstrap")
	}
	var compatible bool
	if err := db.QueryRowContext(ctx, `SELECT
 (SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='cluster_zones' AND column_name IN ('cluster_id','zone_id','lifecycle_state','access_state'))=4
 AND (SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='node_groups' AND column_name IN ('naming_provider','development_stage','naming_region','naming_az','naming_group'))=5
 AND NOT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND ((table_name='clusters' AND column_name='zone_id') OR (table_name='node_pools' AND column_name='team_id')))
 AND EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='node_groups'::regclass AND confrelid='cluster_zones'::regclass AND contype='f' AND cardinality(conkey)=2)`).Scan(&compatible); err != nil {
		return fmt.Errorf("check topology schema readiness: %w", err)
	}
	if !compatible {
		return fmt.Errorf("Crane topology schema is incompatible; initialize a fresh database with crane-bootstrap")
	}
	return nil
}
