package service

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/nabutabu/crane-oss/internal/hostcatalog/store"
	"github.com/nabutabu/crane-oss/pkg/api"
)

func TestProtectedHostsRejectLegacyLifecycleMutations(t *testing.T) {
	for _, operation := range []string{"guard", "state", "transition", "provider", "delete"} {
		t.Run(operation, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			mock.ExpectQuery(regexp.QuoteMeta(`SELECT node_group_id IS NOT NULL, protected FROM host WHERE id=$1`)).WithArgs("protected-host").WillReturnRows(sqlmock.NewRows([]string{"managed", "protected"}).AddRow(false, true))
			catalog := NewHostCatalogService(store.NewPostgresHostStore(db))
			ctx := context.Background()
			switch operation {
			case "guard":
				err = catalog.EnsureLegacyLifecycle(ctx, "protected-host")
			case "state":
				err = catalog.UpdateState(ctx, "protected-host", "TERMINATED")
			case "transition":
				err = catalog.TransitionState(ctx, "protected-host", api.HostDraining)
			case "provider":
				err = catalog.UpdateHostProviderID(ctx, "protected-host", "replacement")
			case "delete":
				err = catalog.DeleteHost(ctx, "protected-host")
			}
			if !errors.Is(err, ErrProtectedHost) {
				t.Fatal(err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
