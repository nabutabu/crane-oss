package capacity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/nabutabu/crane-oss/internal/hostcatalog/service"
	"github.com/nabutabu/crane-oss/internal/hostcatalog/store"
	"github.com/nabutabu/crane-oss/pkg/api"
)

// Every test gets an isolated empty schema. CI supplies the database URL;
// local developers can opt in without altering their operational inventory.
func database(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("CRANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set CRANE_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin := stdlib.OpenDB(*cfg)
	schema := "phase1_" + uuid.New().String()[:8]
	if _, err = admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	cfg.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*cfg)
	t.Cleanup(func() {
		db.Close()
		if _, err := admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	if err = InitializeSchema(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if err = CheckSchema(context.Background(), db); err != nil {
		t.Fatalf("schema readiness: %v", err)
	}
	_, err = db.Exec(`
INSERT INTO zones(id,provider,account_id,region,provider_zone_id,lifecycle_state) VALUES('zone','aws','account','us-west-2','us-west-2a','ready');
INSERT INTO clusters(id,kubeadm_version,kubelet_version,container_runtime,cni,lifecycle_state) VALUES('cluster','1.35.0','1.35.0','containerd','cilium','ready');
INSERT INTO cluster_zones(cluster_id,zone_id,lifecycle_state,access_state) VALUES('cluster','zone','ready','approved');
INSERT INTO zone_dominators(zone_id,endpoint,identity) VALUES('zone','https://dominator:44000','spiffe://test.internal/dominator/zone');
INSERT INTO node_pools(id,max_vcpu,max_memory_mib,max_nodes) VALUES('pool',32,65536,8);
INSERT INTO node_groups(naming_provider,development_stage,naming_region,naming_az,naming_group,id,cluster_id,zone_id,node_pool_id,capacity_class,provider_zone_id,min_nodes,max_nodes,template_revision,vcpu,memory_mib,launch_parameters) VALUES('aws','test','us-west-2','a','general','aws-test-us-west-2-a-general','cluster','zone','pool','general','us-west-2a',0,8,'template-1',4,8192,'{"image_id":"ami-pinned","instance_type":"m7i.xlarge"}');`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func request(id string, revision, target int64) api.CapacityRequest {
	return api.CapacityRequest{NodeGroupID: "aws-test-us-west-2-a-general", ClusterID: "cluster", RequestID: id, ExpectedTargetRevision: revision, DesiredTarget: target, TemplateRevision: "template-1"}
}
func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}
func count(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPostgresAtomicMutationReplayAndEqualDeltas(t *testing.T) {
	db := database(t)
	s := NewStore(db)
	ctx := context.Background()
	r, err := s.SetTarget(ctx, request("first", 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := s.SetTarget(ctx, request("first", 0, 1))
	if err != nil || replayed != r {
		t.Fatalf("replay: %#v, %v", replayed, err)
	}
	if _, err = s.SetTarget(ctx, request("first", 0, 2)); !errors.Is(err, ErrReplayMismatch) {
		t.Fatalf("mismatched replay: %v", err)
	}
	if _, err = s.SetTarget(ctx, request("second", 1, 2)); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"capacity_requests", "operations", "host", "infrastructure_reservations"} {
		if n := count(t, db, table); n != 2 {
			t.Errorf("%s count=%d", table, n)
		}
	}
	r, err = s.Target(ctx, "cluster", "aws-test-us-west-2-a-general")
	if err != nil || r.TargetNodes != 2 || r.TargetRevision != 2 {
		t.Fatalf("restart target refresh: %#v %v", r, err)
	}
	if _, err = s.SetTarget(ctx, request("stale", 0, 3)); !errors.Is(err, ErrConflict) {
		t.Fatalf("CAS: %v", err)
	}
	bad := request("foreign", 2, 3)
	bad.ClusterID = "other"
	if _, err = s.SetTarget(ctx, bad); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cluster isolation: %v", err)
	}
	bad = request("template", 2, 3)
	bad.TemplateRevision = "new"
	if _, err = s.SetTarget(ctx, bad); !errors.Is(err, ErrTemplate) {
		t.Fatalf("template validation: %v", err)
	}
}

func TestPostgresNoPartialWrites(t *testing.T) {
	for _, table := range []string{"capacity_requests", "node_groups", "operations", "host", "infrastructure_reservations"} {
		t.Run(table, func(t *testing.T) {
			db := database(t)
			s := NewStore(db)
			mustExec(t, db, `CREATE FUNCTION reject_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected failure'; END $$`)
			mustExec(t, db, `CREATE TRIGGER reject BEFORE INSERT OR UPDATE ON `+table+` FOR EACH ROW EXECUTE FUNCTION reject_write()`)
			if _, err := s.SetTarget(context.Background(), request("fail", 0, 2)); err == nil || !strings.Contains(err.Error(), "injected failure") {
				t.Fatalf("expected injected failure at %s, got %v", table, err)
			}
			for _, name := range []string{"capacity_requests", "operations", "host", "infrastructure_reservations"} {
				if n := count(t, db, name); n != 0 {
					t.Errorf("partial %s writes: %d", name, n)
				}
			}
			r, err := s.Target(context.Background(), "cluster", "aws-test-us-west-2-a-general")
			if err != nil || r.TargetNodes != 0 || r.TargetRevision != 0 {
				t.Fatalf("partial target mutation: %#v %v", r, err)
			}
		})
	}
}

func TestPostgresConcurrentPoolAdmission(t *testing.T) {
	db := database(t)
	s := NewStore(db)
	mustExec(t, db, `UPDATE node_pools SET max_nodes=1; INSERT INTO node_groups SELECT naming_provider,development_stage,naming_region,naming_az,'second','aws-test-us-west-2-a-second',cluster_id,zone_id,node_pool_id,capacity_class,provider_zone_id,target_nodes,target_revision,min_nodes,max_nodes,template_revision,lifecycle_state,vcpu,memory_mib,launch_parameters FROM node_groups WHERE id='aws-test-us-west-2-a-general'`)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, id := range []string{"aws-test-us-west-2-a-general", "aws-test-us-west-2-a-second"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			<-start
			req := request(id, 0, 1)
			req.NodeGroupID = id
			_, err := s.SetTarget(context.Background(), req)
			results <- err
		}(id)
	}
	close(start)
	wg.Wait()
	close(results)
	success, denied := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrLimit) {
			denied++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || denied != 1 || count(t, db, "infrastructure_reservations") != 1 {
		t.Fatalf("success=%d denied=%d", success, denied)
	}
}

func TestPostgresConcurrentCASAndReplay(t *testing.T) {
	for _, sameID := range []bool{false, true} {
		t.Run(fmt.Sprint(sameID), func(t *testing.T) {
			db := database(t)
			s := NewStore(db)
			start := make(chan struct{})
			results := make(chan error, 2)
			for i := 0; i < 2; i++ {
				go func(i int) {
					<-start
					id := fmt.Sprint(i)
					if sameID {
						id = "same"
					}
					_, err := s.SetTarget(context.Background(), request(id, 0, 1))
					results <- err
				}(i)
			}
			close(start)
			e1, e2 := <-results, <-results
			if sameID {
				if e1 != nil || e2 != nil {
					t.Fatalf("replay concurrency: %v %v", e1, e2)
				}
			} else if !((e1 == nil && errors.Is(e2, ErrConflict)) || (e2 == nil && errors.Is(e1, ErrConflict))) {
				t.Fatalf("CAS concurrency: %v %v", e1, e2)
			}
			if count(t, db, "operations") != 1 || count(t, db, "infrastructure_reservations") != 1 {
				t.Fatal("duplicate allocation")
			}
		})
	}
}

func TestPostgresCancellationAndFencing(t *testing.T) {
	db := database(t)
	s := NewStore(db)
	ctx := context.Background()
	if _, err := s.SetTarget(ctx, request("up", 0, 2)); err != nil {
		t.Fatal(err)
	}
	op, err := s.ClaimDominator(ctx, "zone", "provision", "spiffe://test.internal/dominator/zone", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetTarget(ctx, request("down", 1, 0)); err != nil {
		t.Fatal(err)
	}
	var released, retained int
	if err = db.QueryRow(`SELECT count(*) FILTER(WHERE state='released'),count(*) FILTER(WHERE state='reserved') FROM infrastructure_reservations`).Scan(&released, &retained); err != nil {
		t.Fatal(err)
	}
	if released != 1 || retained != 1 {
		t.Fatalf("released=%d retained=%d", released, retained)
	}
	h, err := s.Host(ctx, op.HostID)
	if err != nil || !h.ExcessCapacity || h.State != api.HostRequested || h.Reserved.VCPU != 4 || h.DesiredStateVersion != "template-1" {
		t.Fatalf("excess host: %#v %v", h, err)
	}
	if err = s.BeginLaunch(ctx, op); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `UPDATE operations SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, op.ID)
	if err = s.Renew(ctx, op, time.Minute); !errors.Is(err, ErrFenced) {
		t.Fatalf("expired renew: %v", err)
	}
	newOp, err := s.ClaimDominator(ctx, "zone", "provision", "spiffe://test.internal/dominator/zone", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if newOp.Fence <= op.Fence || newOp.ID != op.ID || newOp.ProviderToken != op.ProviderToken || string(newOp.Parameters) != string(op.Parameters) {
		t.Fatal("recovery changed immutable provider intent")
	}
	if err = s.BeginLaunch(ctx, op); !errors.Is(err, ErrFenced) {
		t.Fatalf("stale launch: %v", err)
	}
	if err = s.RecordLaunch(ctx, op, "i-old"); !errors.Is(err, ErrFenced) {
		t.Fatalf("stale completion: %v", err)
	}
	if err = s.BeginLaunch(ctx, newOp); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordLaunch(ctx, newOp, "i-confirmed"); err != nil {
		t.Fatal(err)
	}
	h, err = s.Host(ctx, op.HostID)
	if err != nil || h.State != api.HostBooting || h.ProviderID != "i-confirmed" || h.Provider != "aws" {
		t.Fatalf("provider completion: %#v %v", h, err)
	}
}

func TestPostgresDecreaseCannotRemoveReadyNode(t *testing.T) {
	db := database(t)
	s := NewStore(db)
	ctx := context.Background()
	if _, err := s.SetTarget(ctx, request("up", 0, 1)); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `UPDATE host SET state='READY'`)
	if _, err := s.SetTarget(ctx, request("down", 1, 0)); !errors.Is(err, ErrFulfilled) {
		t.Fatalf("ready node reduction: %v", err)
	}
	if count(t, db, "capacity_requests") != 1 {
		t.Fatal("failed decrease persisted")
	}
}

func TestPostgresBaselineSchemaCanBeAppliedRepeatedly(t *testing.T) {
	db := database(t)
	ctx := context.Background()
	mustExec(t, db, `INSERT INTO node_pools(id,max_vcpu,max_memory_mib,max_nodes) VALUES('existing-record',4,8192,1)`)
	if err := InitializeSchema(ctx, db); err != nil {
		t.Fatalf("repeat baseline schema setup: %v", err)
	}
	if err := CheckSchema(ctx, db); err != nil {
		t.Fatalf("schema readiness after repeat: %v", err)
	}
	if got := count(t, db, "node_pools"); got != 2 {
		t.Fatalf("repeat setup changed existing records: got %d budgets", got)
	}
	var migrationLedgerExists bool
	if err := db.QueryRow(`SELECT to_regclass('crane_schema_migrations') IS NOT NULL`).Scan(&migrationLedgerExists); err != nil {
		t.Fatal(err)
	}
	if migrationLedgerExists {
		t.Fatal("first-go schema setup created a migration ledger")
	}
}

func TestPostgresSchemaCheckExplainsMissingBootstrap(t *testing.T) {
	db := database(t)
	mustExec(t, db, `DROP TABLE zones CASCADE`)
	err := CheckSchema(context.Background(), db)
	if err == nil || !strings.Contains(err.Error(), "zones") || !strings.Contains(err.Error(), "run crane-bootstrap") {
		t.Fatalf("schema readiness error did not explain bootstrap requirement: %v", err)
	}
}

func TestPostgresOperationIntentIsImmutable(t *testing.T) {
	db := database(t)
	s := NewStore(db)
	ctx := context.Background()
	if _, err := s.SetTarget(ctx, request("first", 0, 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE operations SET provider_token='replacement-token'`); err == nil {
		t.Fatal("provider token changed")
	}
	if _, err := db.Exec(`UPDATE operations SET parameters='{}'`); err == nil {
		t.Fatal("launch parameters changed")
	}
	mustExec(t, db, `UPDATE node_groups SET template_revision='template-2',launch_parameters='{"image_id":"other"}'`)
	op, err := s.ClaimDominator(ctx, "zone", "provision", "spiffe://test.internal/dominator/zone", time.Minute)
	if err != nil || !strings.Contains(string(op.Parameters), "ami-pinned") {
		t.Fatalf("old intent inherited new template: %#v %v", op, err)
	}
}

func TestPostgresLegacyLifecycleCannotMutateManagedCapacity(t *testing.T) {
	db := database(t)
	s := NewStore(db)
	ctx := context.Background()
	if _, err := s.SetTarget(ctx, request("up", 0, 1)); err != nil {
		t.Fatal(err)
	}
	var hostID string
	if err := db.QueryRow(`SELECT id FROM host`).Scan(&hostID); err != nil {
		t.Fatal(err)
	}
	catalog := service.NewHostCatalogService(store.NewPostgresHostStore(db))
	for _, err := range []error{catalog.TransitionState(ctx, hostID, api.HostReady), catalog.UpdateState(ctx, hostID, string(api.HostTerminated)), catalog.UpdateHostProviderID(ctx, hostID, "other"), catalog.DeleteHost(ctx, hostID)} {
		if !errors.Is(err, service.ErrManagedCapacity) {
			t.Fatalf("legacy mutation did not respect operation authority: %v", err)
		}
	}
	h, err := s.Host(ctx, hostID)
	if err != nil || h.State != api.HostRequested || h.ProviderID != "" {
		t.Fatalf("legacy mutation altered managed host: %#v %v", h, err)
	}
}

func TestPostgresAdmissionPolicy(t *testing.T) {
	for _, test := range []struct {
		name, sql string
		expected  error
	}{
		{"pool_cpu", `UPDATE node_pools SET max_vcpu=3`, ErrLimit},
		{"pool_memory", `UPDATE node_pools SET max_memory_mib=8191`, ErrLimit},
		{"membership_not_ready", `UPDATE cluster_zones SET lifecycle_state='initializing'`, ErrInactive},
		{"membership_revoked", `UPDATE cluster_zones SET access_state='revoked'`, ErrInactive},
		{"pool_nodes", `UPDATE node_pools SET max_nodes=0`, ErrLimit},
		{"zone_disabled", `UPDATE zones SET enabled=false`, ErrInactive},
		{"cluster_not_ready", `UPDATE clusters SET lifecycle_state='requested'`, ErrInactive},
		{"pool_disabled", `UPDATE node_pools SET enabled=false`, ErrInactive},
		{"group_not_active", `UPDATE node_groups SET lifecycle_state='disabled'`, ErrInactive},
		{"group_max", `UPDATE node_groups SET max_nodes=0`, ErrLimit},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := database(t)
			mustExec(t, db, test.sql)
			if _, err := NewStore(db).SetTarget(context.Background(), request("denied", 0, 1)); !errors.Is(err, test.expected) {
				t.Fatalf("expected %v, got %v", test.expected, err)
			}
			if count(t, db, "capacity_requests") != 0 || count(t, db, "operations") != 0 {
				t.Fatal("denied admission left durable writes")
			}
		})
	}
}

func TestPostgresBootstrapAndRemovalOwnership(t *testing.T) {
	db := database(t)
	s := NewStore(db)
	ctx := context.Background()
	id, err := s.CreateZoneBootstrap(ctx, "zone", []byte(`{"image":"pinned"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateZoneBootstrap(ctx, "zone", []byte(`{}`)); err == nil {
		t.Fatal("duplicate active bootstrap")
	}
	if count(t, db, "operations") != 1 {
		t.Fatal("orphan bootstrap operation")
	}
	op, err := s.Claim(ctx, "zone", "create_zone", "bootstrap", time.Minute)
	if err != nil || op.ID != id {
		t.Fatalf("claim bootstrap: %v", err)
	}
	for _, phase := range []api.ZoneBootstrapPhase{api.ZoneDatabaseProvisioning, api.ZoneDatabaseReady, api.ZoneSpireHostProvisioning, api.ZoneSpireInstalling, api.ZoneSpireValidating, api.ZoneControlPlaneInitializing, api.ZoneNetworkInstalling, api.ZoneDominatorInstalling, api.ZoneValidating} {
		if err = s.BootstrapPhase(ctx, op, phase, false); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.BootstrapPhase(ctx, op, api.ZoneReady, false); err == nil {
		t.Fatal("unvalidated bootstrap handoff")
	}
	if err = s.BootstrapPhase(ctx, op, api.ZoneReady, true); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetTarget(ctx, request("up", 0, 1)); err != nil {
		t.Fatal(err)
	}
	provision, err := s.ClaimDominator(ctx, "zone", "provision", "spiffe://test.internal/dominator/zone", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BeginLaunch(ctx, provision); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordLaunch(ctx, provision, "i-node"); err != nil {
		t.Fatal(err)
	}
	removal := api.NodeRemovalOperation{Operation: api.Operation{NodeGroupID: "aws-test-us-west-2-a-general", HostID: provision.HostID}, NodeName: "node", ProviderID: "i-node", Reason: "maintenance", PreserveDesiredCapacity: true, DrainDeadline: time.Now().Add(time.Hour)}
	if _, err = s.CreateRemoval(ctx, "cluster", 1, removal); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateRemoval(ctx, "cluster", 1, removal); err == nil {
		t.Fatal("duplicate removal ownership")
	}
	if count(t, db, "node_removal_operations") != 1 || count(t, db, "operations") != 3 {
		t.Fatal("partial duplicate removal")
	}
}
