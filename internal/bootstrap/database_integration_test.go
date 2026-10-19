package bootstrap

import (
	"context"
	"io"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestDatabaseInitializationAndPendingSeedingAreRepeatable(t *testing.T) {
	dsn := os.Getenv("CRANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set CRANE_TEST_DATABASE_URL for PostgreSQL integration tests")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin := stdlib.OpenDB(*cfg)
	defer admin.Close()
	schema := "bootstrap_" + uuid.New().String()[:8]
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); err != nil {
			t.Error(err)
		}
	}()
	cfg.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*cfg)
	defer db.Close()
	in, err := FirstZone(runConfig())
	if err != nil {
		t.Fatal(err)
	}
	in.Zone.Provider, in.Zone.AccountID, in.Zone.Region, in.Zone.ProviderZoneID = "unit", "account", "region", "zone"
	in.Group.ProviderZoneID = "zone"
	in.Group.LaunchParameters = []byte(`{"image":"pinned"}`)
	for i := 0; i < 2; i++ {
		if err := InitializeDatabase(context.Background(), db, in, true, NewReporter(io.Discard, false)); err != nil {
			t.Fatal(err)
		}
	}
	var zone, cluster string
	var target int64
	if err := db.QueryRow(`SELECT z.lifecycle_state,c.lifecycle_state,g.target_nodes FROM zones z JOIN clusters c ON c.zone_id=z.id JOIN node_groups g ON g.cluster_id=c.id WHERE z.id=$1`, in.Zone.ID).Scan(&zone, &cluster, &target); err != nil {
		t.Fatal(err)
	}
	if zone != "initializing" || cluster != "initializing" || target != 0 {
		t.Fatalf("unexpected initial state: %s %s %d", zone, cluster, target)
	}
}
