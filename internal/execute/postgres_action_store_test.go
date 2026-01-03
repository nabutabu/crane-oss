package execute_test

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/nabutabu/crane-oss/internal/execute"
)

// ------------------- Enqueue -------------------
func TestPostgresActionStore_Enqueue(t *testing.T) {
	tests := []struct {
		name    string
		action  *execute.Action
		wantErr bool
	}{
		{
			name: "success",
			action: &execute.Action{
				HostID: "1",
				Type:   "restart",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, _ := sqlmock.New()
			defer db.Close()
			store := execute.PostgresActionStore{DB: *db}

			if tt.action != nil {
				mock.ExpectQuery("INSERT INTO actions").
					WithArgs(tt.action.HostID, tt.action.Type).
					WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(123))
			}

			gotErr := store.Enqueue(context.Background(), tt.action)
			if gotErr != nil {
				if !tt.wantErr {
					t.Errorf("Enqueue() failed: %v", gotErr)
				}
				return
			}
			if tt.wantErr {
				t.Fatal("Enqueue() succeeded unexpectedly")
			}

			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("unfulfilled expectations: %v", err)
			}
		})
	}
}

// ------------------- Next -------------------
func TestPostgresActionStore_Next(t *testing.T) {
	tests := []struct {
		name     string
		mockRows *sqlmock.Rows
		wantID   int
		wantHost string
		wantType string
		wantErr  bool
	}{
		{
			name: "success",
			mockRows: sqlmock.NewRows([]string{"id", "hostid", "attempts", "type"}).
				AddRow(1, "42", 1, "restart"),
			wantID:   1,
			wantHost: "42",
			wantType: "restart",
			wantErr:  false,
		},
		{
			name:     "no rows",
			mockRows: sqlmock.NewRows([]string{"id", "hostid", "attempts", "type"}),
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, _ := sqlmock.New()
			defer db.Close()
			store := execute.PostgresActionStore{DB: *db}

			mock.ExpectQuery("UPDATE actions").WillReturnRows(tt.mockRows)

			record, err := store.Next(context.Background())
			if (err != nil) != tt.wantErr {
				t.Errorf("Next() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil {
				return
			}

			if record.ID != tt.wantID || record.HostID != tt.wantHost || record.Type != execute.ActionType(tt.wantType) {
				t.Errorf("Next() returned wrong record: %+v", record)
			}
			if record.Status != execute.ActionRunning {
				t.Errorf("Next() status = %v; want %v", record.Status, execute.ActionRunning)
			}

			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("unfulfilled expectations: %v", err)
			}
		})
	}
}

// ------------------- MarkDone -------------------
func TestPostgresActionStore_MarkDone(t *testing.T) {
	tests := []struct {
		name    string
		id      int
		wantErr bool
	}{
		{
			name:    "success",
			id:      123,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, _ := sqlmock.New()
			defer db.Close()
			store := execute.PostgresActionStore{DB: *db}

			mock.ExpectExec("UPDATE actions SET status='done'").
				WithArgs(tt.id).
				WillReturnResult(sqlmock.NewResult(0, 1))

			gotErr := store.MarkDone(context.Background(), tt.id)
			if (gotErr != nil) != tt.wantErr {
				t.Errorf("MarkDone() error = %v, wantErr %v", gotErr, tt.wantErr)
			}

			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("unfulfilled expectations: %v", err)
			}
		})
	}
}

// ------------------- MarkFailed -------------------
func TestPostgresActionStore_MarkFailed(t *testing.T) {
	tests := []struct {
		name    string
		id      int
		wantErr bool
	}{
		{
			name:    "success",
			id:      123,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, _ := sqlmock.New()
			defer db.Close()
			store := execute.PostgresActionStore{DB: *db}

			mock.ExpectExec("UPDATE actions SET status='failed'").
				WithArgs(tt.id).
				WillReturnResult(sqlmock.NewResult(0, 1))

			gotErr := store.MarkFailed(context.Background(), tt.id)
			if (gotErr != nil) != tt.wantErr {
				t.Errorf("MarkFailed() error = %v, wantErr %v", gotErr, tt.wantErr)
			}

			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("unfulfilled expectations: %v", err)
			}
		})
	}
}
