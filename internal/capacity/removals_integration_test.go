package capacity

import (
	"context"
	"errors"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/nabutabu/crane-oss/internal/adapter/autoscaler"
	"github.com/nabutabu/crane-oss/pkg/api"
)

func removalFixture(t *testing.T) (*Store, api.NodeRemovalRequest) {
	t.Helper()
	db := database(t)
	s := NewStore(db)
	if _, err := s.SetTarget(context.Background(), request("seed", 0, 2)); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `UPDATE host SET provider='aws',providerid='i-'||id,state='READY'`)
	rows, err := db.Query(`SELECT id,providerid FROM host ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	req := api.NodeRemovalRequest{NodeGroupID: "group", RequestID: "delete-handler", ExpectedTargetRevision: 1}
	for rows.Next() {
		var id, provider string
		if err := rows.Scan(&id, &provider); err != nil {
			t.Fatal(err)
		}
		req.Nodes = append(req.Nodes, api.RemovalNode{HostID: id, NodeName: "node-" + id, ProviderID: "aws:///us-west-2a/" + provider})
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return s, req
}

func TestPostgresNamedRemovalAtomicReplay(t *testing.T) {
	s, req := removalFixture(t)
	ctx := context.Background()
	if err := s.CreateAutoscalerRemovals(ctx, "other", req); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if err := s.CreateAutoscalerRemovals(ctx, "cluster", req); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAutoscalerRemovals(ctx, "cluster", req); err != nil {
		t.Fatalf("lost-response replay: %v", err)
	}
	target, err := s.Target(ctx, "cluster", "group")
	if err != nil || target.TargetNodes != 0 || target.TargetRevision != 2 || count(t, s.DB, "node_removal_operations") != 2 {
		t.Fatalf("wrong removal result: %+v %v", target, err)
	}
	var unreleased int
	if err := s.DB.QueryRow(`SELECT count(*) FROM credit_reservations WHERE state<>'released'`).Scan(&unreleased); err != nil || unreleased != 2 {
		t.Fatalf("credits released before termination: %d %v", unreleased, err)
	}
	req.Nodes[0].NodeName = "changed"
	if err := s.CreateAutoscalerRemovals(ctx, "cluster", req); !errors.Is(err, ErrReplayMismatch) {
		t.Fatalf("changed replay: %v", err)
	}
}

func TestPostgresNamedRemovalRollback(t *testing.T) {
	for _, stage := range []string{"identity", "operation_write"} {
		t.Run(stage, func(t *testing.T) {
			s, req := removalFixture(t)
			if stage == "identity" {
				req.Nodes[1].ProviderID = "aws:///us-west-2a/foreign"
			} else {
				mustExec(t, s.DB, `CREATE FUNCTION reject_removal() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected removal failure'; END $$`)
				mustExec(t, s.DB, `CREATE TRIGGER reject BEFORE INSERT ON node_removal_operations FOR EACH ROW EXECUTE FUNCTION reject_removal()`)
			}
			if err := s.CreateAutoscalerRemovals(context.Background(), "cluster", req); err == nil {
				t.Fatal("invalid removal succeeded")
			}
			target, err := s.Target(context.Background(), "cluster", "group")
			if err != nil || target.TargetNodes != 2 || target.TargetRevision != 1 || count(t, s.DB, "node_removal_operations") != 0 || count(t, s.DB, "operations") != 2 {
				t.Fatalf("partial batch commit: %+v %v", target, err)
			}
		})
	}
}

func TestPostgresNamedRemovalConcurrentCAS(t *testing.T) {
	s, req := removalFixture(t)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, id := range []string{"one", "two"} {
		wg.Go(func() {
			r := req
			r.RequestID = id
			results <- s.CreateAutoscalerRemovals(context.Background(), "cluster", r)
		})
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 || count(t, s.DB, "node_removal_operations") != 2 {
		t.Fatal("concurrent deletion changed target twice")
	}
}

func TestPostgresAdapterHTTPAdmissionAndReplay(t *testing.T) {
	s, removal := removalFixture(t)
	server := httptest.NewServer(NewAPI(s, "", "test-only", "cluster").Handler())
	defer server.Close()
	client, err := autoscaler.NewHTTPClient(server.URL, "test-only", "cluster")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	target, err := client.Target(ctx, "group")
	if err != nil || target.TargetNodes != 2 || target.TargetRevision != 1 {
		t.Fatalf("target read: %+v %v", target, err)
	}
	if err := client.RemoveNodes(ctx, removal); err != nil {
		t.Fatal(err)
	}
	if err := client.RemoveNodes(ctx, removal); err != nil {
		t.Fatal(err)
	}
	target, err = client.Target(ctx, "group")
	if err != nil || target.TargetNodes != 0 || target.TargetRevision != 2 {
		t.Fatalf("removal replay: %+v %v", target, err)
	}
	req := request("http-scale", 2, 1)
	if _, err := client.SetTarget(ctx, req); err != nil {
		t.Fatal(err)
	}
	if _, err := client.SetTarget(ctx, req); err != nil {
		t.Fatal(err)
	}
	target, err = client.Target(ctx, "group")
	if err != nil || target.TargetNodes != 1 || target.TargetRevision != 3 {
		t.Fatalf("capacity replay: %+v %v", target, err)
	}
}
