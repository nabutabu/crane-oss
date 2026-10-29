package capacity

import (
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestAdapterClusterBinding(t *testing.T) {
	for _, tc := range []struct {
		token, cluster, bound string
		want                  int
	}{
		{"", "cluster", "cluster", 401}, {"wrong", "cluster", "cluster", 401},
		{"test-only", "other", "cluster", 403}, {"test-only", "cluster", "", 401},
	} {
		r := httptest.NewRequest("GET", "/v1/node-groups/aws-test-us-west-2-a-general/target", nil)
		r.Header.Set("Authorization", "Bearer "+tc.token)
		r.Header.Set("X-Crane-Cluster-ID", tc.cluster)
		w := httptest.NewRecorder()
		principals := map[string]string{}
		if tc.bound != "" {
			principals[tc.bound] = "test-only"
		}
		NewAPI(nil, "", principals).Handler().ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("identity gate: got %d want %d", w.Code, tc.want)
		}
	}
}

func TestTargetHTTPReadAndSafeErrors(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
	}{{nil, 200}, {sql.ErrNoRows, 404}, {errors.New("password=secret database detail"), 500}} {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		query := mock.ExpectQuery("SELECT target_nodes,target_revision FROM node_groups").WithArgs("aws-test-us-west-2-a-general", "cluster")
		if tc.err != nil {
			query.WillReturnError(tc.err)
		} else {
			query.WillReturnRows(sqlmock.NewRows([]string{"target_nodes", "target_revision"}).AddRow(2, 5))
		}
		r := httptest.NewRequest(http.MethodGet, "/v1/node-groups/aws-test-us-west-2-a-general/target", nil)
		r.Header.Set("Authorization", "Bearer test-only")
		r.Header.Set("X-Crane-Cluster-ID", "cluster")
		w := httptest.NewRecorder()
		NewAPI(NewStore(db), "", map[string]string{"cluster": "test-only"}).Handler().ServeHTTP(w, r)
		if w.Code != tc.want || strings.Contains(w.Body.String(), "secret") {
			t.Fatalf("unsafe response: %d %s", w.Code, w.Body)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
		db.Close()
	}
}
