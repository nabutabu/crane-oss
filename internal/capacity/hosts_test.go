package capacity

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/nabutabu/crane-oss/pkg/api"
)

var hostListColumns = []string{"id", "provider", "providerid", "region", "zone", "role", "state", "health", "protected", "node_group_id", "node_pool_id", "desired", "observed"}

func TestHostListingAuthenticationAndFilters(t *testing.T) {
	for _, tc := range []struct {
		query, token, configured string
		want                     int
	}{
		{"?provider=aws&region=us-west-2", "", "admin", 401},
		{"?provider=aws&region=us-west-2", "adapter", "admin", 401},
		{"?provider=aws&region=us-west-2", "admin", "", 401},
		{"", "admin", "admin", 400},
		{"?provider=aws", "admin", "admin", 400},
		{"?region=us-west-2", "admin", "admin", 400},
		{"?provider=%20&region=us-west-2", "admin", "admin", 400},
		{"?provider=aws&region=", "admin", "admin", 400},
		{"?provider=aws&provider=other&region=us-west-2", "admin", "admin", 400},
		{"?provider=aws&region=west&region=east", "admin", "admin", 400},
	} {
		r := httptest.NewRequest(http.MethodGet, "/v1/hosts"+tc.query, nil)
		r.Header.Set("Authorization", "Bearer "+tc.token)
		w := httptest.NewRecorder()
		NewAPI(nil, tc.configured, "adapter", "cluster").Handler().ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s: got %d want %d", tc.query, w.Code, tc.want)
		}
	}
}

func TestHostListingHTTPAndSafeErrors(t *testing.T) {
	for _, scenario := range []string{"host", "empty", "query-error", "scan-error", "row-error"} {
		t.Run(scenario, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			rows := sqlmock.NewRows(hostListColumns)
			values := []driver.Value{"deployment/bootstrap/platform", "aws", "provider-id", "us-west-2", "zone", "platform", "READY", "unknown", true, "", "", "3", "2"}
			if scenario != "empty" {
				rows.AddRow(values...)
			}
			if scenario == "scan-error" {
				values[8] = "invalid-bool"
				rows = sqlmock.NewRows(hostListColumns).AddRow(values...)
			}
			if scenario == "row-error" {
				rows.RowError(0, errors.New("synthetic-private-database-detail"))
			}
			query := mock.ExpectQuery("FROM host h JOIN zones z").WithArgs("aws", "us-west-2")
			if scenario == "query-error" {
				query.WillReturnError(errors.New("synthetic-private-database-detail"))
			} else {
				query.WillReturnRows(rows)
			}
			r := httptest.NewRequest(http.MethodGet, "/v1/hosts?provider=aws&region=us-west-2", nil)
			r.Header.Set("Authorization", "Bearer admin")
			w := httptest.NewRecorder()
			NewAPI(NewStore(db), "admin", "", "").Handler().ServeHTTP(w, r)
			if strings.Contains(w.Body.String(), "synthetic-private") {
				t.Fatal("database details exposed")
			}
			if strings.HasSuffix(scenario, "error") {
				if w.Code != http.StatusServiceUnavailable {
					t.Fatal(w.Code)
				}
			} else {
				var result api.HostList
				if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil {
					t.Fatal(w.Code, w.Body.String())
				}
				if scenario == "empty" {
					if result.Hosts == nil || len(result.Hosts) != 0 {
						t.Fatal("empty results must be an array")
					}
				} else if len(result.Hosts) != 1 || result.Hosts[0].ID != "deployment/bootstrap/platform" || !result.Hosts[0].Protected || result.Hosts[0].DesiredStateVersion != "3" {
					t.Fatal("incorrect host summary")
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
