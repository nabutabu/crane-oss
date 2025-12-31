package store_test

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/nabutabu/crane-oss/internal/hostcatalog/store"
	"github.com/nabutabu/crane-oss/pkg/api"
)

func TestPostgresHostStore_Create(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name    string
		host    *api.Host
		mock    func(sqlmock.Sqlmock)
		wantErr bool
	}{
		{
			name: "successfully inserts host",
			host: &api.Host{
				ID:        "host-1",
				Role:      api.Role{Name: "worker"},
				Zone:      "us-west-2a",
				ImageID:   "ami-123",
				State:     "running",
				Health:    "healthy",
				CreatedAt: now,
			},
			mock: func(mock sqlmock.Sqlmock) {
				mock.ExpectExec(
					`INSERT INTO host\(id, role, zone, imageid, state, health, createdat\) VALUES\(\$1, \$2, \$3, \$4, \$5, \$6, \$7\)`,
				).
					WithArgs(
						"host-1",
						"worker",
						"us-west-2a",
						"ami-123",
						"running",
						"healthy",
						now,
					).
					WillReturnResult(sqlmock.NewResult(0, 1))
			},
			wantErr: false,
		},
		{
			name: "database error is returned",
			host: &api.Host{
				ID:        "host-2",
				Role:      api.Role{Name: "control-plane"},
				Zone:      "us-east-1a",
				ImageID:   "ami-456",
				State:     "pending",
				Health:    "unknown",
				CreatedAt: now,
			},
			mock: func(mock sqlmock.Sqlmock) {
				mock.ExpectExec(
					`INSERT INTO host\(id, role, zone, imageid, state, health, createdat\) VALUES\(\$1, \$2, \$3, \$4, \$5, \$6, \$7\)`,
				).
					WillReturnError(errors.New("insert failed"))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("failed to create sqlmock: %v", err)
			}
			defer db.Close()

			store := &store.PostgresHostStore{
				DB: db,
			}

			tt.mock(mock)

			err = store.Create(context.Background(), tt.host)

			if tt.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet sql expectations: %v", err)
			}
		})
	}
}

func TestPostgresHostStore_GetByID(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name    string
		id      string
		mock    func(sqlmock.Sqlmock)
		want    *api.Host
		wantErr bool
	}{
		{
			name: "host found",
			id:   "host-1",
			mock: func(mock sqlmock.Sqlmock) {
				rows := sqlmock.NewRows(
					[]string{"id", "role", "zone", "imageid", "state", "health", "createdat"},
				).AddRow(
					"host-1",
					"worker",
					"us-west-2a",
					"ami-123",
					"running",
					"healthy",
					now,
				)

				mock.ExpectQuery(
					`SELECT id, role, zone, imageid, state, health, createdat FROM host WHERE id = \$1`,
				).
					WithArgs("host-1").
					WillReturnRows(rows)
			},
			want: &api.Host{
				ID:        "host-1",
				Role:      api.Role{Name: "worker"},
				Zone:      "us-west-2a",
				ImageID:   "ami-123",
				State:     "running",
				Health:    "healthy",
				CreatedAt: now,
			},
			wantErr: false,
		},
		{
			name: "host not found",
			id:   "missing-host",
			mock: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(
					`SELECT id, role, zone, imageid, state, health, createdat FROM host WHERE id = \$1`,
				).
					WithArgs("missing-host").
					WillReturnError(sql.ErrNoRows)
			},
			want:    nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("failed to create sqlmock: %v", err)
			}
			defer db.Close()

			store := &store.PostgresHostStore{
				DB: db,
			}

			tt.mock(mock)

			got, err := store.GetByID(context.Background(), tt.id)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetByID() = %+v, want %+v", got, tt.want)
			}

			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet sql expectations: %v", err)
			}
		})
	}
}

func TestPostgresHostStore_UpdateState(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		state   api.HostState
		mock    func(sqlmock.Sqlmock)
		wantErr bool
	}{
		{
			name:  "successfully updates host state",
			id:    "host-1",
			state: api.HostDraining,
			mock: func(mock sqlmock.Sqlmock) {
				mock.ExpectExec(
					`UPDATE host SET state = \$1 WHERE id = \$2`,
				).
					WithArgs(
						api.HostDraining,
						"host-1",
					).
					WillReturnResult(sqlmock.NewResult(0, 1))
			},
			wantErr: false,
		},
		{
			name:  "database error is returned",
			id:    "host-2",
			state: api.HostTerminated,
			mock: func(mock sqlmock.Sqlmock) {
				mock.ExpectExec(
					`UPDATE host SET state = \$1 WHERE id = \$2`,
				).
					WithArgs(
						api.HostTerminated,
						"host-2",
					).
					WillReturnError(errors.New("update failed"))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("failed to create sqlmock: %v", err)
			}
			defer db.Close()

			store := &store.PostgresHostStore{
				DB: db,
			}

			tt.mock(mock)

			err = store.UpdateState(context.Background(), tt.id, tt.state)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet sql expectations: %v", err)
			}
		})
	}
}

func TestPostgresHostStore_UpdateHealth(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		health  string
		mock    func(sqlmock.Sqlmock)
		wantErr bool
	}{
		{
			name:   "successfully updates host health",
			id:     "host-1",
			health: "healthy",
			mock: func(mock sqlmock.Sqlmock) {
				mock.ExpectExec(
					`UPDATE host SET health = \$1 WHERE id = \$2`,
				).
					WithArgs(
						"healthy",
						"host-1",
					).
					WillReturnResult(sqlmock.NewResult(0, 1))
			},
			wantErr: false,
		},
		{
			name:   "database error is returned",
			id:     "host-2",
			health: "unhealthy",
			mock: func(mock sqlmock.Sqlmock) {
				mock.ExpectExec(
					`UPDATE host SET health = \$1 WHERE id = \$2`,
				).
					WithArgs(
						"unhealthy",
						"host-2",
					).
					WillReturnError(errors.New("update failed"))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("failed to create sqlmock: %v", err)
			}
			defer db.Close()

			store := &store.PostgresHostStore{
				DB: db,
			}

			tt.mock(mock)

			err = store.UpdateHealth(context.Background(), tt.id, tt.health)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet sql expectations: %v", err)
			}
		})
	}
}
