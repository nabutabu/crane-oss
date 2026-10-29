// Package capacity owns the durable admission boundary. Controllers execute
// these records directly; they must not copy them into the legacy action queue.
package capacity

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/nabutabu/crane-oss/pkg/api"
)

var (
	ErrInvalid        = errors.New("invalid capacity request")
	ErrConflict       = errors.New("target revision conflict")
	ErrReplayMismatch = errors.New("request ID reused with different parameters")
	ErrForbidden      = errors.New("node group does not belong to authenticated cluster")
	ErrInactive       = errors.New("node group, pool, cluster or zone inactive")
	ErrLimit          = errors.New("capacity limit exceeded")
	ErrTemplate       = errors.New("template revision mismatch")
	ErrFulfilled      = errors.New("target decrease would remove registered capacity")
	ErrFenced         = errors.New("operation lease expired or controller fenced")
)

type Store struct{ DB *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{DB: db} }

type policy struct {
	group     api.NodeGroup
	poolLimit api.Resources
	enabled   bool
}

// SetTarget is the only capacity admission mutation. The caller passes a
// ClusterID obtained from authentication. Group serialization makes replay and
// CAS deterministic; the pool row lock serializes shared reservation checks.
func (s *Store) SetTarget(ctx context.Context, req api.CapacityRequest) (api.CapacityResult, error) {
	var result api.CapacityResult
	if req.NodeGroupID == "" || req.RequestID == "" || req.ClusterID == "" ||
		req.TemplateRevision == "" || req.DesiredTarget < 0 || req.ExpectedTargetRevision < 0 {
		return result, ErrInvalid
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()

	p, err := loadPolicy(ctx, tx, req.NodeGroupID)
	if err != nil {
		return result, err
	}
	if p.group.ClusterID != req.ClusterID {
		return result, ErrForbidden
	}

	var previous api.CapacityRequest
	previous.NodeGroupID = req.NodeGroupID
	previous.RequestID = req.RequestID
	err = tx.QueryRowContext(
		ctx,
		`SELECT cluster_id, expected_revision, desired_target, template_revision, result_revision
		 FROM capacity_requests WHERE group_id=$1 AND request_id=$2`,
		req.NodeGroupID, req.RequestID,
	).Scan(
		&previous.ClusterID, &previous.ExpectedTargetRevision, &previous.DesiredTarget,
		&previous.TemplateRevision, &result.TargetRevision,
	)
	if err == nil {
		if previous != req {
			return api.CapacityResult{}, ErrReplayMismatch
		}
		result.RequestID, result.NodeGroupID, result.TargetNodes = req.RequestID, req.NodeGroupID, req.DesiredTarget
		return result, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	if !p.enabled {
		return result, ErrInactive
	}
	if p.group.TemplateRevision != req.TemplateRevision {
		return result, ErrTemplate
	}
	if p.group.TargetRevision != req.ExpectedTargetRevision {
		return result, ErrConflict
	}
	if req.DesiredTarget < p.group.MinNodes || req.DesiredTarget > p.group.MaxNodes || p.group.TargetRevision == math.MaxInt64 {
		return result, ErrLimit
	}

	// Serialize accounting across every group sharing the pool, including AZs.
	err = tx.QueryRowContext(
		ctx,
		`SELECT max_vcpu,max_memory_mib,max_nodes FROM node_pools WHERE id=$1 FOR UPDATE`,
		p.group.NodePoolID,
	).Scan(&p.poolLimit.VCPU, &p.poolLimit.MemoryMiB, &p.poolLimit.Nodes)
	if err != nil {
		return result, err
	}

	delta := req.DesiredTarget - p.group.TargetNodes
	if delta > 0 {
		if err = checkReservations(ctx, tx, p, delta); err != nil {
			return result, err
		}
	}
	if delta < 0 {
		var registered int64
		err = tx.QueryRowContext(
			ctx,
			`SELECT count(*) FROM host WHERE node_group_id=$1 AND state IN ('REGISTERING','CONVERGING','READY','DRAINING','UNHEALTHY','TERMINATING')`,
			req.NodeGroupID,
		).Scan(&registered)
		if err != nil {
			return result, err
		}
		if req.DesiredTarget < registered {
			return result, ErrFulfilled
		}
	}

	requestID := uuid.NewString()
	result = api.CapacityResult{
		RequestID:      req.RequestID,
		NodeGroupID:    req.NodeGroupID,
		TargetNodes:    req.DesiredTarget,
		TargetRevision: p.group.TargetRevision + 1,
	}
	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO capacity_requests(id,group_id,request_id,cluster_id,expected_revision,desired_target,template_revision,result_revision)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8)`,
		requestID, req.NodeGroupID, req.RequestID, req.ClusterID,
		req.ExpectedTargetRevision, req.DesiredTarget, req.TemplateRevision, result.TargetRevision,
	)
	if err != nil {
		return api.CapacityResult{}, err
	}

	_, err = tx.ExecContext(
		ctx,
		`UPDATE node_groups SET target_nodes=$2,target_revision=$3 WHERE id=$1`,
		req.NodeGroupID, req.DesiredTarget, result.TargetRevision,
	)
	if err != nil {
		return api.CapacityResult{}, err
	}

	for i := int64(0); i < delta; i++ {
		if err = provisionIntent(ctx, tx, p, requestID, result.TargetRevision); err != nil {
			return api.CapacityResult{}, err
		}
	}
	if delta < 0 {
		if err = cancelPending(ctx, tx, req.NodeGroupID, -delta); err != nil {
			return api.CapacityResult{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return api.CapacityResult{}, err
	}
	slog.InfoContext(ctx, "capacity target committed",
		"request_id", req.RequestID,
		"node_group_id", req.NodeGroupID,
		"cluster_id", req.ClusterID,
		"target_nodes", result.TargetNodes,
		"target_revision", result.TargetRevision,
	)
	return result, nil
}

func loadPolicy(ctx context.Context, tx *sql.Tx, id string) (policy, error) {
	var p policy
	err := tx.QueryRowContext(
		ctx,
		`SELECT g.id,g.cluster_id,g.node_pool_id,g.zone_id,g.capacity_class,g.provider_zone_id,g.target_nodes,g.target_revision,g.min_nodes,g.max_nodes,g.template_revision,g.vcpu,g.memory_mib,g.launch_parameters,p.max_vcpu,p.max_memory_mib,p.max_nodes,(g.lifecycle_state='active' AND p.enabled AND z.enabled AND z.lifecycle_state='ready' AND c.lifecycle_state='ready' AND m.lifecycle_state='ready' AND m.access_state='approved')
		 FROM node_groups g
		 JOIN node_pools p ON p.id=g.node_pool_id
		 JOIN zones z ON z.id=g.zone_id
		 JOIN clusters c ON c.id=g.cluster_id
		 JOIN cluster_zones m ON m.cluster_id=g.cluster_id AND m.zone_id=g.zone_id
		 WHERE g.id=$1
		 FOR UPDATE OF g`,
		id,
	).Scan(
		&p.group.ID, &p.group.ClusterID, &p.group.NodePoolID, &p.group.ZoneID,
		&p.group.CapacityClass, &p.group.ProviderZoneID,
		&p.group.TargetNodes, &p.group.TargetRevision, &p.group.MinNodes, &p.group.MaxNodes,
		&p.group.TemplateRevision, &p.group.PerNode.VCPU, &p.group.PerNode.MemoryMiB,
		&p.group.LaunchParameters,
		&p.poolLimit.VCPU, &p.poolLimit.MemoryMiB, &p.poolLimit.Nodes,
		&p.enabled,
	)
	return p, err
}

func checkReservations(ctx context.Context, tx *sql.Tx, p policy, nodes int64) error {
	if nodes > math.MaxInt64/p.group.PerNode.VCPU || nodes > math.MaxInt64/p.group.PerNode.MemoryMiB {
		return ErrLimit
	}
	add := api.Resources{
		VCPU:      nodes * p.group.PerNode.VCPU,
		MemoryMiB: nodes * p.group.PerNode.MemoryMiB,
		Nodes:     nodes,
	}
	scopes := []struct {
		id    string
		query string
		limit api.Resources
	}{
		{
			p.group.NodePoolID,
			`SELECT COALESCE(sum(r.vcpu),0),COALESCE(sum(r.memory_mib),0),COALESCE(sum(r.nodes),0)
			 FROM infrastructure_reservations r
			 JOIN node_groups g ON g.id=r.group_id
			 WHERE g.node_pool_id=$1 AND r.state<>'released'`,
			p.poolLimit,
		},
	}
	for _, scope := range scopes {
		var used api.Resources
		if err := tx.QueryRowContext(ctx, scope.query, scope.id).Scan(&used.VCPU, &used.MemoryMiB, &used.Nodes); err != nil {
			return err
		}
		if add.VCPU > scope.limit.VCPU-used.VCPU ||
			add.MemoryMiB > scope.limit.MemoryMiB-used.MemoryMiB ||
			add.Nodes > scope.limit.Nodes-used.Nodes {
			return ErrLimit
		}
	}
	return nil
}

func provisionIntent(ctx context.Context, tx *sql.Tx, p policy, requestID string, revision int64) error {
	hostID, opID := uuid.NewString(), uuid.NewString()
	_, err := tx.ExecContext(
		ctx,
		`INSERT INTO operations(id,kind,zone_id,group_id,host_id,capacity_request_id,target_revision,provider_token,parameters)
		 VALUES($1,'provision',$2,$3,$4,$5,$6,$7,$8)`,
		opID, p.group.ZoneID, p.group.ID, hostID, requestID, revision, uuid.NewString(), []byte(p.group.LaunchParameters),
	)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO host(id,zone,state,health,createdat,node_group_id,node_pool_id,crane_zone,operation_id,desired_state_version,reserved_vcpu,reserved_memory_mib,role,imageid)
		 VALUES($1,$2::text,'REQUESTED','unknown',clock_timestamp(),$3,$4,$2::text,$5,$6,$7,$8,$9,COALESCE($10::jsonb->>'image_id',''))`,
		hostID, p.group.ZoneID, p.group.ID, p.group.NodePoolID, opID,
		p.group.TemplateRevision, p.group.PerNode.VCPU, p.group.PerNode.MemoryMiB,
		p.group.CapacityClass, []byte(p.group.LaunchParameters),
	)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO infrastructure_reservations(id,group_id,capacity_request_id,host_id,vcpu,memory_mib,state)
		 VALUES($1,$2,$3,$4,$5,$6,'reserved')`,
		uuid.NewString(), p.group.ID, requestID, hostID,
		p.group.PerNode.VCPU, p.group.PerNode.MemoryMiB,
	)
	return err
}

func cancelPending(ctx context.Context, tx *sql.Tx, groupID string, count int64) error {
	// A leased operation may already have an external effect. Never release its
	// credits merely because its lease expired; recover using its persisted token.
	rows, err := tx.QueryContext(
		ctx,
		`SELECT id,host_id FROM operations
		 WHERE group_id=$1 AND kind='provision' AND state='pending' AND lease_owner IS NULL
		 ORDER BY created_at DESC,id
		 LIMIT $2
		 FOR UPDATE`,
		groupID, count,
	)
	if err != nil {
		return err
	}
	type intent struct{ op, host string }
	var intents []intent
	for rows.Next() {
		var i intent
		if err = rows.Scan(&i.op, &i.host); err != nil {
			rows.Close()
			return err
		}
		intents = append(intents, i)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}

	for _, i := range intents {
		if _, err = tx.ExecContext(
			ctx,
			`UPDATE operations SET state='cancelled',fence=fence+1,updated_at=clock_timestamp() WHERE id=$1`,
			i.op,
		); err != nil {
			return err
		}
		if _, err = tx.ExecContext(
			ctx,
			`UPDATE host SET state='TERMINATED',reserved_vcpu=0,reserved_memory_mib=0,updatedat=clock_timestamp() WHERE id=$1`,
			i.host,
		); err != nil {
			return err
		}
		if _, err = tx.ExecContext(
			ctx,
			`UPDATE infrastructure_reservations SET state='released' WHERE host_id=$1`,
			i.host,
		); err != nil {
			return err
		}
	}

	if remaining := count - int64(len(intents)); remaining > 0 {
		_, err = tx.ExecContext(
			ctx,
			`UPDATE host SET excess_capacity=true
			 WHERE id IN (
			   SELECT h.id FROM host h
			   JOIN operations o ON o.id=h.operation_id
			   WHERE h.node_group_id=$1 AND h.state IN ('REQUESTED','PROVISIONING','BOOTING')
			     AND NOT h.excess_capacity AND o.state<>'cancelled'
			   ORDER BY h.createdat DESC,h.id
			   LIMIT $2
			 )`,
			groupID, remaining,
		)
	}
	return err
}

func (s *Store) Target(ctx context.Context, clusterID, groupID string) (api.CapacityResult, error) {
	var r api.CapacityResult
	r.NodeGroupID = groupID
	err := s.DB.QueryRowContext(
		ctx,
		`SELECT target_nodes,target_revision FROM node_groups WHERE id=$1 AND cluster_id=$2`,
		groupID, clusterID,
	).Scan(&r.TargetNodes, &r.TargetRevision)
	return r, err
}

// Claim is reserved for provider-neutral zone bootstrap, before a zone
// Dominator exists. Cluster work must use ClaimDominator.
func (s *Store) Claim(ctx context.Context, zoneID, kind, owner string, duration time.Duration) (api.Operation, error) {
	if kind != "create_zone" {
		return api.Operation{}, ErrForbidden
	}
	return s.claim(ctx, zoneID, kind, owner, duration, false)
}

// ClaimDominator recovers zone execution only for the exact registered
// Dominator and an approved, ready cluster-zone membership.
func (s *Store) ClaimDominator(ctx context.Context, zoneID, kind, identity string, duration time.Duration) (api.Operation, error) {
	if kind != "provision" && kind != "remove_node" {
		return api.Operation{}, ErrForbidden
	}
	return s.claim(ctx, zoneID, kind, identity, duration, true)
}

// claim recovers expired work with a monotonically increasing fence. Provider
// tokens and parameters never change on recovery, including lost responses.
func (s *Store) claim(ctx context.Context, zoneID, kind, owner string, duration time.Duration, authorize bool) (api.Operation, error) {
	var op api.Operation
	if zoneID == "" || owner == "" || duration < time.Millisecond || duration > 24*time.Hour {
		return op, errors.New("invalid operation lease")
	}
	err := s.DB.QueryRowContext(
		ctx,
		`UPDATE operations
		 SET state=CASE WHEN state='launching' THEN 'launching' ELSE 'leased' END,
		     lease_owner=$3,
		     lease_until=clock_timestamp()+($4 * interval '1 millisecond'),
		     fence=fence+1,
		     updated_at=clock_timestamp()
		 WHERE id=(
		   SELECT id FROM operations
		   WHERE operations.zone_id=$1 AND operations.kind=$2
		     AND (state='pending' OR (state IN ('leased','launching') AND lease_until<=clock_timestamp()))
		     AND (NOT $5 OR EXISTS (
		       SELECT 1 FROM zone_dominators d
		       JOIN node_groups g ON g.id=operations.group_id AND g.zone_id=operations.zone_id
		       JOIN cluster_zones m ON m.cluster_id=g.cluster_id AND m.zone_id=g.zone_id
		       WHERE d.zone_id=operations.zone_id AND d.identity=$3
		         AND m.lifecycle_state='ready' AND m.access_state='approved'
		     ))
		   ORDER BY created_at,id
		   FOR UPDATE SKIP LOCKED
		   LIMIT 1
		 )
		 RETURNING id,kind,zone_id,COALESCE(group_id,''),COALESCE(host_id,''),COALESCE(capacity_request_id,''),target_revision,state,provider_token,parameters,lease_owner,lease_until,fence,last_error,created_at,updated_at`,
		zoneID, kind, owner, duration.Milliseconds(), authorize,
	).Scan(
		&op.ID, &op.Kind, &op.ZoneID, &op.NodeGroupID, &op.HostID, &op.CapacityRequestID,
		&op.TargetRevision, &op.State, &op.ProviderToken, &op.Parameters,
		&op.LeaseOwner, &op.LeaseUntil, &op.Fence, &op.LastError, &op.CreatedAt, &op.UpdatedAt,
	)
	return op, err
}

func (s *Store) Renew(ctx context.Context, op api.Operation, duration time.Duration) error {
	if duration < time.Millisecond || duration > 24*time.Hour {
		return errors.New("invalid operation lease")
	}
	r, err := s.DB.ExecContext(
		ctx,
		`UPDATE operations
		 SET lease_until=clock_timestamp()+($4 * interval '1 millisecond'),updated_at=clock_timestamp()
		 WHERE id=$1 AND lease_owner=$2 AND fence=$3
		   AND lease_until>clock_timestamp() AND state IN ('leased','launching')
		   AND (kind='create_zone' OR EXISTS (
		     SELECT 1 FROM node_groups g
		     JOIN cluster_zones m ON m.cluster_id=g.cluster_id AND m.zone_id=g.zone_id
		     JOIN zone_dominators d ON d.zone_id=g.zone_id
		     WHERE g.id=operations.group_id AND g.zone_id=operations.zone_id
		       AND d.identity=operations.lease_owner
		       AND m.lifecycle_state='ready' AND m.access_state='approved'
		   ))`,
		op.ID, op.LeaseOwner, op.Fence, duration.Milliseconds(),
	)
	return fencedResult(r, err)
}

// BeginLaunch must succeed immediately before a provider call. The provider
// call must use op.ProviderToken and op.Parameters exactly as persisted.
func (s *Store) BeginLaunch(ctx context.Context, op api.Operation) error {
	r, err := s.DB.ExecContext(
		ctx,
		`UPDATE operations
		 SET state='launching',updated_at=clock_timestamp()
		 WHERE id=$1 AND lease_owner=$2 AND fence=$3
		   AND lease_until>clock_timestamp() AND state IN ('leased','launching')
		   AND EXISTS (
		     SELECT 1 FROM node_groups g
		     JOIN cluster_zones m ON m.cluster_id=g.cluster_id AND m.zone_id=g.zone_id
		     JOIN zone_dominators d ON d.zone_id=g.zone_id
		     WHERE g.id=operations.group_id AND g.zone_id=operations.zone_id
		       AND d.identity=operations.lease_owner
		       AND m.lifecycle_state='ready' AND m.access_state='approved'
		   )`,
		op.ID, op.LeaseOwner, op.Fence,
	)
	return fencedResult(r, err)
}

func fencedResult(r sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrFenced
	}
	return nil
}

// RecordLaunch records a confirmed provider allocation without marking the host
// ready. Registration/convergence and readiness gates belong to Phase 2.
func (s *Store) RecordLaunch(ctx context.Context, op api.Operation, providerID string) error {
	if op.Kind != "provision" || providerID == "" {
		return errors.New("invalid provider result")
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	r, err := tx.ExecContext(
		ctx,
		`UPDATE operations
		 SET state='succeeded',lease_owner=NULL,lease_until=NULL,updated_at=clock_timestamp()
		 WHERE id=$1 AND kind='provision' AND host_id=$4
		   AND lease_owner=$2 AND fence=$3
		   AND lease_until>clock_timestamp() AND state='launching'`,
		op.ID, op.LeaseOwner, op.Fence, op.HostID,
	)
	if err = fencedResult(r, err); err != nil {
		return err
	}

	_, err = tx.ExecContext(
		ctx,
		`UPDATE host SET providerid=$2,provider=z.provider,state='BOOTING',updatedat=clock_timestamp()
		 FROM zones z
		 WHERE host.id=$1 AND z.id=host.crane_zone`,
		op.HostID, providerID,
	)
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(
		ctx,
		`UPDATE infrastructure_reservations SET state='committed' WHERE host_id=$1 AND state='reserved'`,
		op.HostID,
	)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// CreateZoneBootstrap persists layer-zero work; it does not execute bootstrap.
func (s *Store) CreateZoneBootstrap(ctx context.Context, zoneID string, parameters json.RawMessage) (string, error) {
	if !json.Valid(parameters) {
		return "", errors.New("invalid bootstrap parameters")
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	id := uuid.NewString()
	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO operations(id,kind,zone_id,provider_token,parameters)
		 VALUES($1,'create_zone',$2,$3,$4)`,
		id, zoneID, uuid.NewString(), []byte(parameters),
	)
	if err != nil {
		return "", err
	}

	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO zone_bootstrap_operations(operation_id,zone_id) VALUES($1,$2)`,
		id, zoneID,
	)
	if err != nil {
		return "", err
	}

	if err = tx.Commit(); err != nil {
		return "", err
	}
	return id, nil
}
