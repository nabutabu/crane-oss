package poolstore

import (
	"context"
	"database/sql"

	"github.com/nabutabu/crane-oss/pkg/api"
)

type PostgresPoolStore struct {
	DB *sql.DB
}

func NewPostgresPoolStore(DB *sql.DB) *PostgresPoolStore {
	return &PostgresPoolStore{
		DB: DB,
	}
}

func (store *PostgresPoolStore) ListPools(ctx context.Context) ([]*api.Pool, error) {
	query := `SELECT id, teamid, role FROM pool`
	rows, err := store.DB.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var pools []*api.Pool
	for rows.Next() {
		var pool api.Pool
		var role sql.NullString
		err := rows.Scan(&pool.ID, &pool.TeamID, &role)
		if err != nil {
			return nil, err
		}
		if role.Valid {
			pool.Role = api.Role{Name: role.String}
		}
		pools = append(pools, &pool)
	}

	if err = rows.Err(); err != nil {
		return nil, err
	}

	return pools, nil
}

func (store *PostgresPoolStore) GetPool(ctx context.Context, id string) (*api.Pool, error) {
	query := `SELECT id, teamid, role FROM pool WHERE id = $1`
	var pool api.Pool
	var role sql.NullString
	err := store.DB.QueryRowContext(ctx, query, id).Scan(&pool.ID, &pool.TeamID, &role)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if role.Valid {
		pool.Role = api.Role{Name: role.String}
	}
	return &pool, nil
}
