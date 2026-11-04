package capacity

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/nabutabu/crane-oss/pkg/api"
	"time"
)

type ProvisionBinding struct {
	NodeName, NodeUID, ProviderID, ContractHash string
	ReleaseStarted                              bool
	ExpectedState                               api.ExpectedHostState
}

func (s *Store) BindProvisionNode(ctx context.Context, op api.Operation, b ProvisionBinding) error {
	if b.NodeName != api.WorkerNodeName(op.HostID) || b.NodeUID == "" || b.ProviderID == "" || len(b.ContractHash) != 64 {
		return ErrInvalid
	}
	r, err := s.DB.ExecContext(ctx, `INSERT INTO worker_provisioning_state(host_id,operation_id,node_name,node_uid,provider_id,contract_hash)
 SELECT h.id,o.id,$5,$6,$7,$8 FROM operations o JOIN host h ON h.id=o.host_id
 JOIN node_groups g ON g.id=o.group_id JOIN cluster_zones m ON m.cluster_id=g.cluster_id AND m.zone_id=g.zone_id
 WHERE o.id=$1 AND o.lease_owner=$2 AND o.fence=$3 AND o.host_id=$4 AND o.kind='provision' AND o.state='launching'
 AND o.lease_until>clock_timestamp() AND h.state='REGISTERING' AND m.lifecycle_state='ready' AND m.access_state='approved'
 ON CONFLICT(host_id) DO UPDATE SET host_id=EXCLUDED.host_id
 WHERE (worker_provisioning_state.operation_id,worker_provisioning_state.node_name,worker_provisioning_state.node_uid,worker_provisioning_state.provider_id,worker_provisioning_state.contract_hash)=
 (EXCLUDED.operation_id,EXCLUDED.node_name,EXCLUDED.node_uid,EXCLUDED.provider_id,EXCLUDED.contract_hash)`, op.ID, op.LeaseOwner, op.Fence, op.HostID, b.NodeName, b.NodeUID, b.ProviderID, b.ContractHash)
	return fencedResult(r, err)
}

func (s *Store) ProvisionBinding(ctx context.Context, op api.Operation) (ProvisionBinding, error) {
	var b ProvisionBinding
	var raw []byte
	err := s.DB.QueryRowContext(ctx, `SELECT node_name,node_uid,provider_id,contract_hash,release_started,expected_state FROM worker_provisioning_state WHERE host_id=$1 AND operation_id=$2`, op.HostID, op.ID).Scan(&b.NodeName, &b.NodeUID, &b.ProviderID, &b.ContractHash, &b.ReleaseStarted, &raw)
	if err != nil {
		return b, err
	}
	if json.Unmarshal(raw, &b.ExpectedState) != nil {
		return b, ErrInvalid
	}
	return b, nil
}

// PinProvisionExpectedState preserves the same request after policy changes or restarts.
func (s *Store) PinProvisionExpectedState(ctx context.Context, op api.Operation, in api.ExpectedHostState) (api.ExpectedHostState, error) {
	if in.Revision != 1 || in.Version == "" || len(in.Version) > 128 || in.Secret != nil {
		return api.ExpectedHostState{}, ErrInvalid
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return api.ExpectedHostState{}, ErrInvalid
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return api.ExpectedHostState{}, err
	}
	defer tx.Rollback()
	var allowed bool
	err = tx.QueryRowContext(ctx, `SELECT true FROM operations o JOIN host h ON h.id=o.host_id WHERE o.id=$1 AND o.host_id=$2 AND o.lease_owner=$3 AND o.fence=$4 AND o.lease_until>clock_timestamp() AND o.state='launching' AND h.state='REGISTERING' FOR UPDATE OF o,h`, op.ID, op.HostID, op.LeaseOwner, op.Fence).Scan(&allowed)
	if errors.Is(err, sql.ErrNoRows) {
		return api.ExpectedHostState{}, ErrFenced
	}
	if err != nil {
		return api.ExpectedHostState{}, err
	}
	err = tx.QueryRowContext(ctx, `UPDATE worker_provisioning_state SET expected_state=CASE WHEN expected_state='{}'::jsonb THEN $3::jsonb ELSE expected_state END WHERE host_id=$1 AND operation_id=$2 RETURNING expected_state`, op.HostID, op.ID, raw).Scan(&raw)
	if err != nil {
		return api.ExpectedHostState{}, err
	}
	var pinned api.ExpectedHostState
	if json.Unmarshal(raw, &pinned) != nil || pinned.Revision != 1 || pinned.Version == "" || pinned.Secret != nil {
		return pinned, ErrInvalid
	}
	if _, err = tx.ExecContext(ctx, `UPDATE host SET desired_state_version=$2 WHERE id=$1`, op.HostID, pinned.Version); err != nil {
		return pinned, err
	}
	return pinned, tx.Commit()
}

// PrepareProvisionRelease commits recovery intent before any external release.
func (s *Store) PrepareProvisionRelease(ctx context.Context, op api.Operation, desiredVersion string) error {
	r, err := s.DB.ExecContext(ctx, `UPDATE worker_provisioning_state b SET release_started=true FROM operations o,host h,node_groups g,cluster_zones m
 WHERE b.host_id=h.id AND b.operation_id=o.id AND o.host_id=h.id AND g.id=o.group_id AND m.cluster_id=g.cluster_id AND m.zone_id=g.zone_id
 AND o.id=$1 AND o.lease_owner=$2 AND o.fence=$3 AND o.lease_until>clock_timestamp() AND o.state='launching' AND h.state='CONVERGING'
 AND h.desired_state_version=$4 AND $4<>'' AND b.expected_state->>'version'=$4 AND b.expected_state->>'revision'='1'
 AND m.lifecycle_state='ready' AND m.access_state='approved'`, op.ID, op.LeaseOwner, op.Fence, desiredVersion)
	return fencedResult(r, err)
}

// FinalizeProvision serializes the bounded Kubernetes release with lifecycle ownership.
// A failed/unknown release leaves CONVERGING and durable recovery intent.
func (s *Store) FinalizeProvision(ctx context.Context, op api.Operation, desiredVersion string, release func(context.Context) error) error {
	if release == nil {
		return ErrInvalid
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var until time.Time
	err = tx.QueryRowContext(ctx, `SELECT o.lease_until FROM operations o JOIN host h ON h.id=o.host_id
 JOIN worker_provisioning_state b ON b.host_id=h.id AND b.operation_id=o.id
 JOIN node_groups g ON g.id=o.group_id JOIN cluster_zones m ON m.cluster_id=g.cluster_id AND m.zone_id=g.zone_id
 JOIN zones z ON z.id=g.zone_id JOIN clusters c ON c.id=g.cluster_id
 WHERE o.id=$1 AND o.lease_owner=$2 AND o.fence=$3 AND o.host_id=$4 AND o.kind='provision' AND o.state='launching'
 AND o.lease_until>clock_timestamp()+interval '1 second' AND h.state='CONVERGING' AND b.release_started
 AND h.desired_state_version=$5 AND b.expected_state->>'version'=$5 AND b.expected_state->>'revision'='1'
 AND m.lifecycle_state='ready' AND m.access_state='approved' AND z.enabled AND z.lifecycle_state='ready' AND c.lifecycle_state='ready'
 FOR UPDATE OF o,h,b,m,z,c`, op.ID, op.LeaseOwner, op.Fence, op.HostID, desiredVersion).Scan(&until)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrFenced
	}
	if err != nil {
		return err
	}
	deadline := time.Now().Add(20 * time.Second)
	if until.Add(-time.Second).Before(deadline) {
		deadline = until.Add(-time.Second)
	}
	stepCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	stepCtx = context.WithValue(stepCtx, provisionReleaseKey{}, op)
	if err = release(stepCtx); err != nil {
		return err
	}
	if stepCtx.Err() != nil {
		return stepCtx.Err()
	}
	r, err := tx.ExecContext(ctx, `UPDATE host h SET state='READY',observed_state_version=desired_state_version,updatedat=clock_timestamp() FROM operations o WHERE o.id=$1 AND o.host_id=h.id AND o.lease_until>clock_timestamp() AND o.fence=$2 AND h.state='CONVERGING'`, op.ID, op.Fence)
	if err = fencedResult(r, err); err != nil {
		return err
	}
	r, err = tx.ExecContext(ctx, `UPDATE operations SET state='succeeded',lease_owner=NULL,lease_until=NULL,updated_at=clock_timestamp() WHERE id=$1 AND fence=$2`, op.ID, op.Fence)
	if err = fencedResult(r, err); err != nil {
		return err
	}
	return tx.Commit()
}

type provisionReleaseKey struct{}

func ProvisionReleaseAuthorized(ctx context.Context, op api.Operation) bool {
	held, ok := ctx.Value(provisionReleaseKey{}).(api.Operation)
	return ok && held.ID == op.ID && held.Fence == op.Fence && held.LeaseOwner == op.LeaseOwner
}
