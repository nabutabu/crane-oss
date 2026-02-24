package problem_test

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"github.com/nabutabu/crane-oss/internal/badhost/problem"
)

func TestPostgresProblemStore_RecordProblem(t *testing.T) {
	tests := []struct {
		name    string
		hostID  string
		problem problem.Problem
		mockErr error
		wantErr bool
	}{
		{
			name:   "successful insert",
			hostID: "host-123",
			problem: problem.Problem{
				Type:     problem.ProblemType("disk_full"),
				Severity: problem.ProblemSeverity("critical"),
				Details:  `{"disk":"/dev/sda1","used":"95%"}`,
			},
			wantErr: false,
		},
		{
			name:   "database insert failure",
			hostID: "host-456",
			problem: problem.Problem{
				Type:     problem.ProblemType("heartbeat_missing"),
				Severity: problem.ProblemSeverity("warning"),
				Details:  "no heartbeat received for 5 minutes",
			},
			mockErr: errors.New("insert failed"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()

			store := problem.PostgresProblemStore{
				DB: db,
			}

			query := regexp.QuoteMeta(`
INSERT INTO host_problems (host_id, problem_type, severity, details, detected_at)
VALUES ($1, $2, $3, $4, $5)
`)

			expect := mock.ExpectExec(query).
				WithArgs(
					tt.hostID,
					tt.problem.Type,
					tt.problem.Severity,
					tt.problem.Details,
					sqlmock.AnyArg(), // detected_at
				)

			if tt.mockErr != nil {
				expect.WillReturnError(tt.mockErr)
			} else {
				expect.WillReturnResult(sqlmock.NewResult(1, 1))
			}

			err = store.RecordProblem(context.Background(), tt.hostID, tt.problem)

			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestPostgresProblemStore_GetUnresolvedProblems(t *testing.T) {
	tests := []struct {
		name     string
		rows     *sqlmock.Rows
		queryErr error
		wantLen  int
		wantErr  bool
	}{
		{
			name: "returns unresolved problems",
			rows: sqlmock.NewRows([]string{
				"id", "host_id", "problem_type", "severity",
				"detected_at", "resolved_at", "details",
			}).
				AddRow(
					1,
					"host-1",
					"disk_full",
					"critical",
					time.Now(),
					nil,
					"disk usage exceeded 95%",
				).
				AddRow(
					2,
					"host-2",
					"heartbeat_missing",
					"warning",
					time.Now(),
					nil,
					"no heartbeat for 5 minutes",
				),
			wantLen: 2,
			wantErr: false,
		},
		{
			name: "no unresolved problems",
			rows: sqlmock.NewRows([]string{
				"id", "host_id", "problem_type", "severity",
				"detected_at", "resolved_at", "details",
			}),
			wantLen: 0,
			wantErr: false,
		},
		{
			name:     "query failure",
			queryErr: errors.New("db unavailable"),
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()

			store := problem.PostgresProblemStore{DB: db}

			query := regexp.QuoteMeta(`
SELECT id, host_id, problem_type, severity, detected_at, resolved_at, details
FROM host_problems
WHERE resolved_at IS NULL
`)

			if tt.queryErr != nil {
				mock.ExpectQuery(query).
					WillReturnError(tt.queryErr)
			} else {
				mock.ExpectQuery(query).
					WillReturnRows(tt.rows)
			}

			problems, err := store.GetUnresolvedProblems(context.Background())

			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Len(t, problems, tt.wantLen)
			}

			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
