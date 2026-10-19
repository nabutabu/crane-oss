package bootstrap

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/nabutabu/crane-oss/internal/capacity"
	"github.com/nabutabu/crane-oss/pkg/api"
)

func InitializeDatabase(ctx context.Context, db *sql.DB, in api.FirstZoneInput, pending bool, reporter *Reporter) error {
	if err := reporter.Run(2, "initialize Crane schema", func() (string, error) {
		return "", capacity.InitializeSchema(ctx, db)
	}); err != nil {
		return err
	}
	return reporter.Run(2, "seed first zone", func() (string, error) {
		store := capacity.NewStore(db)
		var err error
		if pending {
			err = store.SeedFirstZonePending(ctx, in.Zone, in.Cluster, in.Budget, in.Pool, in.Group)
		} else {
			err = store.SeedFirstZone(ctx, in.Zone, in.Cluster, in.Budget, in.Pool, in.Group)
		}
		return fmt.Sprintf("zone %s, target_nodes=0", in.Zone.ID), err
	})
}
