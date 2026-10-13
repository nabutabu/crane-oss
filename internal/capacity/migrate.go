package capacity

import (
	"context"
	"database/sql"
	_ "embed"
)

//go:embed migrations/001_foundation.sql
var foundationSQL string

// Migrate installs the additive foundation atomically. Run before starting
// controllers; the advisory lock serializes concurrent binary startups.
func Migrate(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(724163001)`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS crane_schema_migrations(version integer PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT clock_timestamp())`); err != nil {
		return err
	}
	var exists bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM crane_schema_migrations WHERE version=1)`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err = tx.ExecContext(ctx, foundationSQL); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO crane_schema_migrations(version) VALUES(1)`); err != nil {
			return err
		}
	}
	return tx.Commit()
}
