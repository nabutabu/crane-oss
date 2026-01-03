package execute

import (
	"context"
	"database/sql"
	"log"
)

type PostgresActionStore struct {
	DB sql.DB
}

func (store *PostgresActionStore) Enqueue(ctx context.Context, action *Action) error {
	log.Println("/PostgresActionStore/Enqueue")

	query := `
        INSERT INTO actions (hostid, status, attempts, createdat, type)
        VALUES ($1, 'pending', 0, NOW(), $2)
				RETURNING id
    `

	var id int
	err := store.DB.QueryRow(query, action.HostID, action.Type).Scan(&id)
	if err != nil {
		return err
	}
	log.Printf("New task id: %d", id)

	return nil
}

func (store *PostgresActionStore) Next(ctx context.Context) (*ActionRecord, error) {
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
        RETURNING id, hostid, attempts, type
    `
	err := store.DB.QueryRow(query).Scan(
		&record.ID,
		&record.HostID,
		&record.Attempts,
		&record.Type,
	)
	if err != nil {
		return nil, err
	}

	record.Status = ActionRunning
	return &record, nil
}

func (store *PostgresActionStore) MarkDone(ctx context.Context, id int) error {
	// Mark it done
	_, err := store.DB.Exec("UPDATE actions SET status='done', updatedat=NOW() WHERE id=$1", id)
	if err != nil {
		return err
	}
	return nil
}

func (store *PostgresActionStore) MarkFailed(ctx context.Context, id int) error {
	// Mark it failed
	_, err := store.DB.Exec("UPDATE actions SET status='failed', updatedat=NOW() WHERE id=$1", id)
	if err != nil {
		return err
	}
	return nil
}
