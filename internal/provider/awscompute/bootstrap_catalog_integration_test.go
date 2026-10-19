package awscompute

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/nabutabu/crane-oss/internal/capacity"
)

func TestBootstrapCatalogSeedRerunAndConflict(t *testing.T) {
	dsn := os.Getenv("CRANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("PostgreSQL fixture required")
	}
	psql, err := exec.LookPath("psql")
	if err != nil {
		t.Skip("psql required")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin := stdlib.OpenDB(*cfg)
	schema := "subd_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		defer admin.Close()
		if _, err := admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); err != nil {
			t.Error(err)
		}
	})
	cfg.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*cfg)
	defer db.Close()
	if err := capacity.InitializeSchema(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	script, err := RenderSeedHostsScript(testConfig(), testInfra(), testAgentHosts())
	if err != nil {
		t.Fatal(err)
	}
	_, sql, ok := strings.Cut(script, "<<'SQL'\n")
	if !ok {
		t.Fatal("missing SQL")
	}
	sql, _, _ = strings.Cut(sql, "\nSQL\n")
	run := func() error {
		cmd := exec.Command(psql, "-X", "-q", "-v", "ON_ERROR_STOP=1", "-v", "zone=zone")
		cmd.Stdin = strings.NewReader(sql)
		// libpq settings avoid putting connection credentials in process arguments.
		cmd.Env = append(os.Environ(), "PGHOST="+cfg.Host, "PGPORT="+fmt.Sprint(cfg.Port), "PGUSER="+cfg.User, "PGPASSWORD="+cfg.Password, "PGDATABASE="+cfg.Database, "PGSSLMODE=disable", "PGOPTIONS=-c search_path="+schema)
		return cmd.Run()
	}
	for i := 0; i < 2; i++ {
		if err := run(); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM host WHERE protected AND node_group_id IS NULL AND health='unknown' AND lastseenheartbeat IS NULL`).Scan(&count); err != nil || count != 2 {
		t.Fatal("seed did not converge", count, err)
	}
	if _, err := db.Exec(`UPDATE host SET providerid='conflicting' WHERE role='spire'`); err != nil {
		t.Fatal(err)
	}
	if err := run(); err == nil {
		t.Fatal("conflicting host registration accepted")
	}
}
