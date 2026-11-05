package capacity

import (
	"context"
	"errors"
	"github.com/nabutabu/crane-oss/pkg/api"
	"strings"
	"testing"
	"time"
)

func handoffFixture(t *testing.T) (*Store, api.Operation) {
	t.Helper()
	s := NewStore(database(t))
	ctx := context.Background()
	if _, err := s.SetTarget(ctx, request("handoff", 0, 1)); err != nil {
		t.Fatal(err)
	}
	op, err := s.ClaimDominator(ctx, "zone", "provision", "spiffe://test.internal/dominator/zone", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	op.ClusterID = "cluster"
	if err = s.BeginLaunch(ctx, op); err != nil {
		t.Fatal(err)
	}
	if err = s.SetProvisionState(ctx, op, api.HostRequested, api.HostProvisioning); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordLaunchWithAddress(ctx, op, "i-worker", "10.0.0.8", "aws:///us-west-2a/i-worker"); err != nil {
		t.Fatal(err)
	}
	if err = s.SetProvisionState(ctx, op, api.HostBooting, api.HostRegistering); err != nil {
		t.Fatal(err)
	}
	b := ProvisionBinding{NodeName: api.WorkerNodeName(op.HostID), NodeUID: "uid", ProviderID: "aws:///us-west-2a/i-worker", ContractHash: strings.Repeat("a", 64)}
	if err = s.BindProvisionNode(ctx, op, b); err != nil {
		t.Fatal(err)
	}
	b.NodeUID = "replacement"
	if s.BindProvisionNode(ctx, op, b) == nil {
		t.Fatal("replacement Node UID accepted")
	}
	if _, err = s.PinProvisionExpectedState(ctx, op, api.ExpectedHostState{Revision: 1, Version: "v1"}); err != nil {
		t.Fatal(err)
	}
	pinned, err := s.PinProvisionExpectedState(ctx, op, api.ExpectedHostState{Revision: 1, Version: "v2"})
	if err != nil || pinned.Version != "v1" {
		t.Fatal("expected state changed during retry", err)
	}
	if err = s.SetProvisionState(ctx, op, api.HostRegistering, api.HostConverging); err != nil {
		t.Fatal(err)
	}
	return s, op
}

func TestPostgresReadinessHandoffRecoversUnknownRelease(t *testing.T) {
	s, op := handoffFixture(t)
	ctx := context.Background()
	called := 0
	release := func(context.Context) error { called++; return nil }
	if err := s.FinalizeProvision(ctx, op, "v1", release); !errors.Is(err, ErrFenced) || called != 0 {
		t.Fatal("release without durable intent", err)
	}
	if err := s.PrepareProvisionRelease(ctx, op, "v1"); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected unknown release outcome")
	if err := s.FinalizeProvision(ctx, op, "v1", func(context.Context) error { called++; return injected }); !errors.Is(err, injected) {
		t.Fatal(err)
	}
	host, err := s.Host(ctx, op.HostID)
	if err != nil || host.State != api.HostConverging {
		t.Fatal("unknown outcome produced READY", err)
	}
	b, err := s.ProvisionBinding(ctx, op)
	if err != nil || !b.ReleaseStarted {
		t.Fatal("release recovery intent lost", err)
	}
	mustExec(t, s.DB, `UPDATE operations SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, op.ID)
	if err := s.FinalizeProvision(ctx, op, "v1", release); !errors.Is(err, ErrFenced) || called != 1 {
		t.Fatal("stale owner released worker", err)
	}
	recovered, err := s.ClaimDominator(ctx, "zone", "provision", op.LeaseOwner, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	recovered.ClusterID = "cluster"
	if recovered.ID != op.ID || recovered.Fence <= op.Fence {
		t.Fatal("recovery changed operation or did not advance fence")
	}
	if err = s.FinalizeProvision(ctx, recovered, "v1", release); err != nil {
		t.Fatal(err)
	}
	host, err = s.Host(ctx, op.HostID)
	if err != nil || host.State != api.HostReady || host.ObservedStateVersion != "v1" {
		t.Fatal("recovery failed to confirm expected state", err)
	}
	if count(t, s.DB, "host") != 1 || count(t, s.DB, "operations") != 1 || count(t, s.DB, "infrastructure_reservations") != 1 {
		t.Fatal("recovery duplicated inventory or released credits")
	}
}

func TestPostgresReadinessHandoffRejectsRevocationAndVersionDrift(t *testing.T) {
	s, op := handoffFixture(t)
	ctx := context.Background()
	if err := s.PrepareProvisionRelease(ctx, op, "wrong-version"); !errors.Is(err, ErrFenced) {
		t.Fatal("wrong desired version accepted", err)
	}
	if err := s.PrepareProvisionRelease(ctx, op, "v1"); err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.DB, `UPDATE cluster_zones SET access_state='revoked' WHERE cluster_id='cluster' AND zone_id='zone'`)
	if err := s.FinalizeProvision(ctx, op, "v1", func(context.Context) error { t.Fatal("revoked membership invoked scheduling release"); return nil }); !errors.Is(err, ErrFenced) {
		t.Fatal(err)
	}
}
