package execute

import (
	"context"
	"database/sql"
	"log/slog"
)

type PostgresActionStore struct {
	DB *sql.DB
}

func NewPostgresActionStore(DB *sql.DB) *PostgresActionStore {
	return &PostgresActionStore{
		DB: DB,
	}
}

func (store *PostgresActionStore) Enqueue(ctx context.Context, action *ActionRecord) error {
	slog.DebugContext(ctx, "enqueuing action", "host_id", action.HostID, "action", action.Type)

	query := `
        INSERT INTO actions (hostid, status, attempts, createdat, type)
        VALUES ($1, 'pending', 0, NOW(), $2)
				RETURNING id
    `

	var id int
	err := store.DB.QueryRowContext(ctx, query, action.HostID, action.Type).Scan(&id)
	if err != nil {
		return err
	}
	slog.DebugContext(ctx, "action enqueued", "action_id", id, "host_id", action.HostID)

	return nil
}

func (store *PostgresActionStore) Next(ctx context.Context) (*ActionRecord, error) {
	slog.DebugContext(ctx, "fetching next action")
	var record ActionRecord
	query := `
        UPDATE actions
        SET status = 'running', updatedat = NOW(), attempts = attempts + 1
        WHERE id = (
            SELECT id
            FROM actions
            WHERE status = 'pending'
            ORDER BY createdat
            LIMIT 1
            FOR UPDATE SKIP LOCKED
        )
        RETURNING id, hostid, attempts, type, provider
    `
	err := store.DB.QueryRowContext(ctx, query).Scan(
		&record.ID,
		&record.HostID,
		&record.Attempts,
		&record.Type,
		&record.Provider,
	)
	if err != nil {
		return nil, err
	}

	record.Status = ActionRunning
	return &record, nil
}

func (store *PostgresActionStore) MarkDone(ctx context.Context, id int) error {
	slog.DebugContext(ctx, "marking action done", "action_id", id)
	// Mark it done
	_, err := store.DB.ExecContext(ctx, "UPDATE actions SET status='done', updatedat=NOW() WHERE id=$1", id)
	if err != nil {
		return err
	}
	return nil
}

func (store *PostgresActionStore) MarkFailed(ctx context.Context, id int) error {
	slog.DebugContext(ctx, "marking action failed", "action_id", id)
	// Mark it failed
	_, err := store.DB.ExecContext(ctx, "UPDATE actions SET status='failed', updatedat=NOW() WHERE id=$1", id)
	if err != nil {
		return err
	}
	return nil
}
