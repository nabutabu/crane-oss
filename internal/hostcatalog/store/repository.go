package store

import (
	"context"
	"database/sql"
	"log"

	"github.com/nabutabu/crane-oss/pkg/api"
)

type PostgresHostStore struct {
	DB *sql.DB
}

func NewPostgresHostStore(DB *sql.DB) *PostgresHostStore {
	return &PostgresHostStore{
		DB: DB,
	}
}

func (store *PostgresHostStore) Create(ctx context.Context, host *api.Host) error {
	log.Println("/PostgresHostStore/Create")
	query := "INSERT INTO host(id, role, zone, imageid, state, health, createdat) VALUES($1, $2, $3, $4, $5, $6, $7)"

	_, err := store.DB.Exec(query, host.ID, host.Role.Name, host.Zone, host.ImageID, host.State, host.Health, host.CreatedAt)
	if err != nil {
		return err
	}

	return nil
}

func (store *PostgresHostStore) GetByID(ctx context.Context, id string) (*api.Host, error) {
	query := `
		SELECT id, role, zone, imageid, state, health, createdat
		FROM host
		WHERE id = $1
	`

	row := store.DB.QueryRowContext(ctx, query, id)

	var h api.Host
	var role string

	err := row.Scan(
		&h.ID,
		&role,
		&h.Zone,
		&h.ImageID,
		&h.State,
		&h.Health,
		&h.CreatedAt,
	)
	if err != nil {
		return nil, err
	}

	h.Role = api.Role{Name: role}
	return &h, nil
}

func (store *PostgresHostStore) UpdateState(ctx context.Context, id string, newState api.HostState) error {
	log.Println("/PostgresHostStore/UpdateState")

	query := "UPDATE host SET state = $1 WHERE id = $2"
	_, err := store.DB.Exec(query, newState, id)
	if err != nil {
		return err
	}

	return nil
}

func (store *PostgresHostStore) UpdateHealth(ctx context.Context, id string, newHealth api.HostHealth) error {
	log.Println("/PostgresHostStore/UpdateHealth")

	query := "UPDATE host SET health = $1 WHERE id = $2"
	_, err := store.DB.Exec(query, newHealth, id)
	if err != nil {
		return err
	}

	return nil
}

func (store *PostgresHostStore) ListHosts(ctx context.Context) ([]*api.Host, error) {
	log.Println("/PostgresHostStore/UpdateHealth")

	query := `SELECT id, role, zone, imageid, state, health, createdat FROM host`
	rows, err := store.DB.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var hosts []*api.Host
	for rows.Next() {
		var host api.Host
		var role string

		err = rows.Scan(
			&host.ID,
			&role,
			&host.Zone,
			&host.ImageID,
			&host.State,
			&host.Health,
			&host.CreatedAt,
		)
		if err != nil {
			return nil, err
		}

		host.Role = api.Role{
			Name: role,
		}
		hosts = append(hosts, &host)
	}

	return hosts, nil
}
