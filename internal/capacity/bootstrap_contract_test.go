package capacity

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestSafeBootstrapParameters(t *testing.T) {
	for _, tc := range []struct {
		json string
		want bool
	}{
		{`{"image":"sha256:abc","secret_ref":"ssm:/deployment/key"}`, true},
		{`{"auth":{"join_token":"private"}}`, false},
		{`{"profiles":[{"password":"private"}]}`, false},
		{`{"password":"private"}`, false},
		{`[]`, false},
		{`{`, false},
	} {
		if got := safeBootstrapParameters([]byte(tc.json)); got != tc.want {
			t.Errorf("safeBootstrapParameters(%s) = %v, want %v", tc.json, got, tc.want)
		}
	}
}

func TestBootstrapStatusRequiresAdmin(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	route := NewAPI(NewStore(db), "admin-test", nil).Handler()
	unauthorized := httptest.NewRecorder()
	route.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/v1/bootstrap/op", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("got %d", unauthorized.Code)
	}
	mock.ExpectQuery("SELECT o.id,o.kind,b.cluster_id").WithArgs("op").WillReturnRows(sqlmock.NewRows([]string{"id", "kind", "cluster_id", "zone_id", "state", "phase"}).AddRow("op", "attach_zone", "cluster", "zone", "pending", "Requested"))
	req := httptest.NewRequest(http.MethodGet, "/v1/bootstrap/op", nil)
	req.Header.Set("Authorization", "Bearer admin-test")
	authorized := httptest.NewRecorder()
	route.ServeHTTP(authorized, req)
	if authorized.Code != http.StatusOK {
		t.Fatalf("got %d: %s", authorized.Code, authorized.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
