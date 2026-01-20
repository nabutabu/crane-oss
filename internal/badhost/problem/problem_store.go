package problem

import (
	"context"
	"database/sql"
	"time"
)

type ProblemStore interface {
	RecordProblem(ctx context.Context, hostID string, problem Problem) error
	GetUnresolvedProblems(ctx context.Context) ([]Problem, error)
	GetRecentProblems(ctx context.Context, hostID string, duration time.Duration) ([]Problem, error)
}
type PostgresProblemStore struct {
	DB *sql.DB
}

func New(db *sql.DB) *PostgresProblemStore {
	return &PostgresProblemStore{
		DB: db,
	}
}

func (store *PostgresProblemStore) RecordProblem(ctx context.Context, hostID string, problem Problem) error {
	query := `INSERT INTO host_problems (host_id, problem_type, severity, details, detected_at)
	VALUES ($1, $2, $3, $4, $5)`

	_, err := store.DB.ExecContext(
		ctx,
		query,
		hostID,
		problem.Type,
		problem.Severity,
		problem.Details, // already a string
		time.Now(),
	)

	return err
}

func (store *PostgresProblemStore) GetUnresolvedProblems(ctx context.Context) ([]Problem, error) {
	query := `SELECT id, host_id, problem_type, severity, detected_at, resolvedat, details
	FROM host_problems
	WHERE resolvedat IS NULL
	GROUP BY host_id`

	var problems []Problem
	rows, err := store.DB.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var problem Problem

		err = rows.Scan(
			&problem.ID,
			&problem.Host_id,
			&problem.Type,
			&problem.Severity,
			&problem.DetectedAt,
			&problem.ResolvedAt,
			&problem.Details,
		)
		if err != nil {
			return nil, err
		}

		problems = append(problems, problem)
	}

	return problems, nil
}

func (store *PostgresProblemStore) GetRecentProblems(ctx context.Context, hostID string, duration time.Duration) ([]Problem, error) {
	return nil, nil
}
