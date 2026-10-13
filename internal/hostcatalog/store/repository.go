package store

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

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
	slog.Debug("creating host", "host_id", host.ID)
	query := "INSERT INTO host(id, role, zone, imageid, state, health, createdat, provider, providerid) VALUES($1, $2, $3, $4, $5, $6, $7, $8, $9)"

	_, err := store.DB.ExecContext(ctx, query, host.ID, host.Role.Name, host.Zone, host.ImageID, host.State, host.Health, host.CreatedAt, host.Provider, host.ProviderID)
	if err != nil {
		return err
	}

	return nil
}

func (store *PostgresHostStore) Delete(ctx context.Context, id string) error {
	slog.Debug("deleting host", "host_id", id)
	query := "DELETE FROM host WHERE id = $1"

	_, err := store.DB.ExecContext(ctx, query, id)

	return err
}

func (store *PostgresHostStore) GetByID(ctx context.Context, id string) (*api.Host, error) {
	query := `
		SELECT id, role, zone, imageid, state, health, createdat, provider, providerID, lastseenheartbeat
		FROM host
		WHERE id = $1
	`

	row := store.DB.QueryRowContext(ctx, query, id)

	var h api.Host
	var role string
	var provider sql.NullString
	var providerID sql.NullString
	var lastSeenHeartbeat sql.NullTime

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
		&lastSeenHeartbeat,
	)
	if err != nil {
		return nil, err
	}

	if provider.Valid {
		h.Provider = provider.String
	} else {
		h.Provider = ""
	}

	if providerID.Valid {
		h.ProviderID = providerID.String
	} else {
		h.ProviderID = ""
	}

	if lastSeenHeartbeat.Valid {
		h.LastSeenHeartbeat = lastSeenHeartbeat.Time
	}

	h.Role = api.Role{Name: role}
	return &h, nil
}

func (store *PostgresHostStore) GetByProviderID(ctx context.Context, id string) (*api.Host, error) {
	query := `
		SELECT id, role, zone, imageid, state, health, createdat, provider, providerID, lastseenheartbeat
		FROM host
		WHERE providerID = $1
	`

	row := store.DB.QueryRowContext(ctx, query, id)

	var h api.Host
	var role string
	var provider sql.NullString
	var providerID sql.NullString
	var lastSeenHeartbeat sql.NullTime

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
		&lastSeenHeartbeat,
	)
	if err != nil {
		return nil, err
	}

	if provider.Valid {
		h.Provider = provider.String
	} else {
		h.Provider = ""
	}

	if providerID.Valid {
		h.ProviderID = providerID.String
	} else {
		h.ProviderID = ""
	}

	if lastSeenHeartbeat.Valid {
		h.LastSeenHeartbeat = lastSeenHeartbeat.Time
	}

	h.Role = api.Role{Name: role}
	return &h, nil
}

func (store *PostgresHostStore) UpdateState(ctx context.Context, id string, newState api.HostState) error {
	slog.Debug("updating host state", "host_id", id, "new_state", newState)

	query := "UPDATE host SET state = $1 WHERE id = $2"
	_, err := store.DB.ExecContext(ctx, query, newState, id)
	if err != nil {
		return err
	}

	return nil
}

func (store *PostgresHostStore) UpdateHealth(ctx context.Context, id string, newHealth api.HostHealth) error {
	slog.Debug("updating host health", "host_id", id, "new_health", newHealth)

	query := "UPDATE host SET health = $1 WHERE id = $2"
	_, err := store.DB.ExecContext(ctx, query, newHealth, id)
	if err != nil {
		return err
	}

	return nil
}

func (store *PostgresHostStore) GetByZone(ctx context.Context, zone string) ([]*api.Host, error) {
	slog.Debug("listing hosts by zone", "zone", zone)

	query := `SELECT id, role, zone, imageid, state, health, createdat, provider, providerid, lastSeenHeartbeat FROM host`
	rows, err := store.DB.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var hosts []*api.Host
	for rows.Next() {
		var host api.Host
		var role string
		var provider sql.NullString
		var providerID sql.NullString
		var lastSeenHeartbeat sql.NullTime

		err = rows.Scan(
			&host.ID,
			&role,
			&host.Zone,
			&host.ImageID,
			&host.State,
			&host.Health,
			&host.CreatedAt,
			&provider,
			&providerID,
			&lastSeenHeartbeat,
		)
		if err != nil {
			return nil, err
		}

		if provider.Valid {
			host.Provider = provider.String
		}
		if providerID.Valid {
			host.ProviderID = providerID.String
		}
		if lastSeenHeartbeat.Valid {
			host.LastSeenHeartbeat = lastSeenHeartbeat.Time
		}

		host.Role = api.Role{
			Name: role,
		}
		hosts = append(hosts, &host)
	}

	return hosts, nil
}

func (store *PostgresHostStore) ListHosts(ctx context.Context) ([]*api.Host, error) {
	slog.Debug("listing hosts")

	query := `SELECT id, role, zone, imageid, state, health, createdat, provider, providerid, lastseenheartbeat FROM host`
	rows, err := store.DB.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var hosts []*api.Host
	for rows.Next() {
		var host api.Host
		var role string
		var provider sql.NullString
		var providerID sql.NullString
		var lastSeenHeartbeat sql.NullTime

		err = rows.Scan(
			&host.ID,
			&role,
			&host.Zone,
			&host.ImageID,
			&host.State,
			&host.Health,
			&host.CreatedAt,
			&provider,
			&providerID,
			&lastSeenHeartbeat,
		)
		if err != nil {
			return nil, err
		}

		if provider.Valid {
			host.Provider = provider.String
		}
		if providerID.Valid {
			host.ProviderID = providerID.String
		}
		if lastSeenHeartbeat.Valid {
			host.LastSeenHeartbeat = lastSeenHeartbeat.Time
		}

		host.Role = api.Role{
			Name: role,
		}
		hosts = append(hosts, &host)
	}

	return hosts, nil
}

func (store *PostgresHostStore) UpdateProviderID(ctx context.Context, hostID string, providerID string) error {
	slog.Debug("updating host provider id", "host_id", hostID)
	query := "UPDATE host set providerID = $1 WHERE id = $2"

	_, err := store.DB.ExecContext(ctx, query, providerID, hostID)

	return err
}

func (store *PostgresHostStore) UpdateLastSeenHeartbeat(ctx context.Context, hostID string, lastSeen time.Time) error {
	slog.Debug("updating last seen heartbeat", "host_id", hostID)
	query := "UPDATE host SET lastSeenHeartbeat = $1 WHERE id = $2"

	_, err := store.DB.ExecContext(ctx, query, lastSeen, hostID)

	return err
}

func (store *PostgresHostStore) UpdateDBConnectionInfo(ctx context.Context, hostID string, endpoint string, port int32, dbname string, username string, secretarn string, rdssgid string) error {
	slog.Debug("updating db connection info", "host_id", hostID)
	query := "UPDATE host SET endpoint = $1, port = $2, dbname = $3, username = $4, secretarn = $5, rdssgid = $6 WHERE id = $7"

	_, err := store.DB.ExecContext(ctx, query, endpoint, port, dbname, username, secretarn, rdssgid, hostID)

	return err
}

func (store *PostgresHostStore) GetByToken(ctx context.Context, token string) (*api.Host, error) {
	slog.Debug("getting host by token")
	query := "SELECT id, role, zone, imageid, state, health, createdat, provider, providerid, lastseenheartbeat FROM host WHERE token = $1"

	row := store.DB.QueryRowContext(ctx, query, token)

	var h api.Host
	var role string
	var provider sql.NullString
	var providerID sql.NullString
	var lastSeenHeartbeat sql.NullTime

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
		&lastSeenHeartbeat,
	)
	if err != nil {
		return nil, err
	}

	if provider.Valid {
		h.Provider = provider.String
	} else {
		h.Provider = ""
	}

	if providerID.Valid {
		h.ProviderID = providerID.String
	} else {
		h.ProviderID = ""
	}

	if lastSeenHeartbeat.Valid {
		h.LastSeenHeartbeat = lastSeenHeartbeat.Time
	}

	h.Role = api.Role{Name: role}
	return &h, nil
}
