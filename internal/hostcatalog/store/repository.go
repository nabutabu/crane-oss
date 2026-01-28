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
	query := "INSERT INTO host(id, role, zone, imageid, state, health, createdat, provider, providerid, assignedpool) VALUES($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)"

	_, err := store.DB.Exec(query, host.ID, host.Role.Name, host.Zone, host.ImageID, host.State, host.Health, host.CreatedAt, host.Provider, host.ProviderID, host.AssignedPool)
	if err != nil {
		return err
	}

	return nil
}

func (store *PostgresHostStore) Delete(ctx context.Context, id string) error {
	log.Printf("/PostgresHostStore/Delete: id: %s\n", id)
	query := "DELETE FROM host WHERE id = $1"

	_, err := store.DB.Exec(query, id)

	return err
}

func (store *PostgresHostStore) GetByID(ctx context.Context, id string) (*api.Host, error) {
	query := `
		SELECT id, role, zone, imageid, state, health, createdat, provider, providerID, assignedpool
		FROM host
		WHERE id = $1
	`

	row := store.DB.QueryRowContext(ctx, query, id)

	var h api.Host
	var role string
	var provider sql.NullString
	var providerID sql.NullString
	var assignedPool sql.NullString

	err := row.Scan(
		&h.ID,
		&role,
		&h.Zone,
		&h.ImageID,
		&h.State,
		&h.Health,
		&h.CreatedAt,
		&provider,
		&providerID,
		&assignedPool,
	)
	if err != nil {
		return nil, err
	}

	if provider.Valid {
		h.Provider = provider.String
	} else {
		h.Provider = "" // or leave unset
	}

	if providerID.Valid {
		h.ProviderID = providerID.String
	} else {
		h.ProviderID = "" // or leave unset
	}

	if assignedPool.Valid {
		h.AssignedPool = &assignedPool.String
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

func (store *PostgresHostStore) GetByZone(ctx context.Context, zone string) ([]*api.Host, error) {
	log.Println("/PostgresHostStore/GetByZone")

	query := `SELECT id, role, zone, imageid, state, health, createdat, provider, providerid, assignedpool FROM host`
	rows, err := store.DB.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var hosts []*api.Host
	for rows.Next() {
		var host api.Host
		var role string
		var assignedPool sql.NullString

		err = rows.Scan(
			&host.ID,
			&role,
			&host.Zone,
			&host.ImageID,
			&host.State,
			&host.Health,
			&host.CreatedAt,
			&host.Provider,
			&host.ProviderID,
			&assignedPool,
		)
		if err != nil {
			return nil, err
		}

		if assignedPool.Valid {
			host.AssignedPool = &assignedPool.String
		}

		host.Role = api.Role{
			Name: role,
		}
		hosts = append(hosts, &host)
	}

	return hosts, nil
}

func (store *PostgresHostStore) ListHosts(ctx context.Context) ([]*api.Host, error) {
	log.Println("/PostgresHostStore/ListHosts")

	query := `SELECT id, role, zone, imageid, state, health, createdat, provider, providerid, assignedpool FROM host`
	rows, err := store.DB.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var hosts []*api.Host
	for rows.Next() {
		var host api.Host
		var role string
		var assignedPool sql.NullString

		err = rows.Scan(
			&host.ID,
			&role,
			&host.Zone,
			&host.ImageID,
			&host.State,
			&host.Health,
			&host.CreatedAt,
			&host.Provider,
			&host.ProviderID,
			&assignedPool,
		)
		if err != nil {
			return nil, err
		}

		if assignedPool.Valid {
			host.AssignedPool = &assignedPool.String
		}

		host.Role = api.Role{
			Name: role,
		}
		hosts = append(hosts, &host)
	}

	return hosts, nil
}

func (store *PostgresHostStore) UpdateProviderID(ctx context.Context, hostID string, providerID string) error {
	log.Println("/PostgresHostStore/UpdateProviderID")
	query := "UPDATE host set providerID = $1 WHERE id = $2"

	_, err := store.DB.Exec(query, providerID, hostID)

	return err
}
