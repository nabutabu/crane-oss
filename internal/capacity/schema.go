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
		"zones", "clusters", "team_budgets", "node_pools", "node_groups",
		"capacity_requests", "host", "operations", "credit_reservations",
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
	return nil
}
