package capacity

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/nabutabu/crane-oss/pkg/api"
)

type topologyWriter interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// immutableInsert accepts an exact retry, but never changes an existing identity.
func immutableInsert(ctx context.Context, db topologyWriter, query string, args ...any) error {
	r, err := db.ExecContext(ctx, query, args...)
	if err != nil {
		return topologyError(err)
	}
	n, err := r.RowsAffected()
	if err == nil && n == 0 {
		return ErrConflict
	}
	return err
}

func topologyError(err error) error {
	var state interface{ SQLState() string }
	if errors.As(err, &state) {
		switch state.SQLState() {
		case "23505":
			return ErrConflict
		case "23503", "23514", "23502":
			return ErrInvalid
		}
	}
	return err
}

func createZone(ctx context.Context, db topologyWriter, z api.ZoneInput, lifecycle string) error {
	if z.ID == "" || z.Provider == "" || z.AccountID == "" || z.Region == "" || z.ProviderZoneID == "" {
		return ErrInvalid
	}
	return immutableInsert(ctx, db, `INSERT INTO zones(id,provider,account_id,region,provider_zone_id,lifecycle_state)
 VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(id) DO UPDATE SET id=EXCLUDED.id
 WHERE (zones.provider,zones.account_id,zones.region,zones.provider_zone_id)=
 (EXCLUDED.provider,EXCLUDED.account_id,EXCLUDED.region,EXCLUDED.provider_zone_id)`,
		z.ID, z.Provider, z.AccountID, z.Region, z.ProviderZoneID, lifecycle)
}

func createCluster(ctx context.Context, db topologyWriter, c api.ClusterInput, lifecycle string, seed bool) error {
	if c.ID == "" || c.KubeadmVersion == "" || c.KubeletVersion == "" || c.ContainerRuntime == "" || c.CNI != "cilium" {
		return ErrInvalid
	}
	return immutableInsert(ctx, db, `INSERT INTO clusters(id,api_server_endpoint,ca_cert_hash,kubeadm_version,kubelet_version,container_runtime,cni,lifecycle_state)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(id) DO UPDATE SET id=EXCLUDED.id
 WHERE (clusters.kubeadm_version,clusters.kubelet_version,clusters.container_runtime,clusters.cni)=
 (EXCLUDED.kubeadm_version,EXCLUDED.kubelet_version,EXCLUDED.container_runtime,EXCLUDED.cni)
 AND (clusters.api_server_endpoint=EXCLUDED.api_server_endpoint OR ($9 AND EXCLUDED.api_server_endpoint=''))
 AND (clusters.ca_cert_hash=EXCLUDED.ca_cert_hash OR ($9 AND EXCLUDED.ca_cert_hash=''))`,
		c.ID, c.APIServerEndpoint, c.CACertHash, c.KubeadmVersion, c.KubeletVersion, c.ContainerRuntime, c.CNI, lifecycle, seed)
}

func createPool(ctx context.Context, db topologyWriter, p api.NodePoolInput) error {
	if p.ID == "" || p.Limits.VCPU < 0 || p.Limits.MemoryMiB < 0 || p.Limits.Nodes < 0 {
		return ErrInvalid
	}
	return immutableInsert(ctx, db, `INSERT INTO node_pools(id,max_vcpu,max_memory_mib,max_nodes)
 VALUES($1,$2,$3,$4) ON CONFLICT(id) DO UPDATE SET id=EXCLUDED.id
 WHERE (node_pools.max_vcpu,node_pools.max_memory_mib,node_pools.max_nodes)=
 (EXCLUDED.max_vcpu,EXCLUDED.max_memory_mib,EXCLUDED.max_nodes)`, p.ID, p.Limits.VCPU, p.Limits.MemoryMiB, p.Limits.Nodes)
}

func createGroup(ctx context.Context, db topologyWriter, g api.NodeGroupInput) error {
	if g.NormalizeID() != nil || g.ClusterID == "" || g.ZoneID == "" || g.NodePoolID == "" ||
		g.CapacityClass == "" || g.TemplateRevision == "" || g.MinNodes < 0 || g.MaxNodes < g.MinNodes ||
		g.PerNode.VCPU <= 0 || g.PerNode.MemoryMiB <= 0 || !json.Valid(g.LaunchParameters) {
		return ErrInvalid
	}
	scheduling, err := json.Marshal(g.Scheduling)
	if err != nil {
		return ErrInvalid
	}
	if g.Scheduling.InstanceType != "" || g.Scheduling.Region != "" || g.Scheduling.Zone != "" || g.Scheduling.Architecture != "" || len(g.Scheduling.Labels) > 0 || len(g.Scheduling.Taints) > 0 {
		if err := validateScheduling(g.Scheduling, g.NodePoolID); err != nil {
			return err
		}
	}
	return immutableInsert(ctx, db, `INSERT INTO node_groups(id,cluster_id,zone_id,node_pool_id,capacity_class,provider_zone_id,min_nodes,max_nodes,template_revision,vcpu,memory_mib,launch_parameters,naming_provider,development_stage,naming_region,naming_az,naming_group,scheduling)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
 ON CONFLICT(id) DO UPDATE SET scheduling=CASE
 WHEN COALESCE(node_groups.scheduling->>'instance_type','')='' AND COALESCE(node_groups.scheduling->>'zone','')=''
 THEN EXCLUDED.scheduling ELSE node_groups.scheduling END
 WHERE (node_groups.cluster_id,node_groups.zone_id,node_groups.node_pool_id,node_groups.capacity_class,node_groups.provider_zone_id,
 node_groups.min_nodes,node_groups.max_nodes,node_groups.template_revision,node_groups.vcpu,node_groups.memory_mib,node_groups.launch_parameters,
 node_groups.naming_provider,node_groups.development_stage,node_groups.naming_region,node_groups.naming_az,node_groups.naming_group)=
 (EXCLUDED.cluster_id,EXCLUDED.zone_id,EXCLUDED.node_pool_id,EXCLUDED.capacity_class,EXCLUDED.provider_zone_id,
 EXCLUDED.min_nodes,EXCLUDED.max_nodes,EXCLUDED.template_revision,EXCLUDED.vcpu,EXCLUDED.memory_mib,EXCLUDED.launch_parameters,
 EXCLUDED.naming_provider,EXCLUDED.development_stage,EXCLUDED.naming_region,EXCLUDED.naming_az,EXCLUDED.naming_group)
 AND (node_groups.scheduling=EXCLUDED.scheduling OR
 (COALESCE(node_groups.scheduling->>'instance_type','')='' AND COALESCE(node_groups.scheduling->>'zone','')=''))`,
		g.ID, g.ClusterID, g.ZoneID, g.NodePoolID, g.CapacityClass, g.ProviderZoneID, g.MinNodes, g.MaxNodes, g.TemplateRevision,
		g.PerNode.VCPU, g.PerNode.MemoryMiB, []byte(g.LaunchParameters), g.Naming.Provider, g.Naming.DevelopmentStage, g.Naming.Region, g.Naming.AZ, g.Naming.Group, scheduling)
}

func normalizeMembership(m *api.ClusterZoneInput) error {
	if m.LifecycleState == "" {
		m.LifecycleState = "requested"
	}
	if m.AccessState == "" {
		m.AccessState = "pending"
	}
	if m.ClusterID == "" || m.ZoneID == "" {
		return ErrInvalid
	}
	switch m.LifecycleState {
	case "requested", "initializing", "ready", "failed":
	default:
		return ErrInvalid
	}
	switch m.AccessState {
	case "pending", "approved", "revoked":
	default:
		return ErrInvalid
	}
	return nil
}

func createMembership(ctx context.Context, db topologyWriter, m api.ClusterZoneInput, seed bool) error {
	if err := normalizeMembership(&m); err != nil {
		return err
	}
	query := `INSERT INTO cluster_zones(cluster_id,zone_id,lifecycle_state,access_state) VALUES($1,$2,$3,$4)
 ON CONFLICT(cluster_id,zone_id) DO UPDATE SET cluster_id=EXCLUDED.cluster_id`
	if !seed {
		query += ` WHERE cluster_zones.lifecycle_state=EXCLUDED.lifecycle_state AND cluster_zones.access_state=EXCLUDED.access_state`
	}
	return immutableInsert(ctx, db, query, m.ClusterID, m.ZoneID, m.LifecycleState, m.AccessState)
}

// SeedFirstZone atomically seeds topology without resetting durable runtime state.
func (s *Store) SeedFirstZone(ctx context.Context, in api.FirstZoneInput) error {
	return s.seedFirstZone(ctx, in, true)
}
func (s *Store) SeedFirstZonePending(ctx context.Context, in api.FirstZoneInput) error {
	return s.seedFirstZone(ctx, in, false)
}

func (s *Store) seedFirstZone(ctx context.Context, in api.FirstZoneInput, ready bool) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := seedFirstZoneTx(ctx, tx, in, ready); err != nil {
		return err
	}
	return tx.Commit()
}

func seedFirstZoneTx(ctx context.Context, tx *sql.Tx, in api.FirstZoneInput, ready bool) error {
	if in.Group.NormalizeID() != nil {
		return ErrInvalid
	}
	if in.Membership.ClusterID != in.Cluster.ID || in.Membership.ZoneID != in.Zone.ID ||
		in.Group.ClusterID != in.Cluster.ID || in.Group.ZoneID != in.Zone.ID || in.Group.NodePoolID != in.Pool.ID ||
		in.Group.MinNodes != 0 {
		return ErrInvalid
	}
	var err error
	// One seed transaction per database; common ordering is group before pool.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(724163002)`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `SELECT id FROM node_groups WHERE id=$1 FOR UPDATE`, in.Group.ID); err != nil {
		return err
	}
	lifecycle := "initializing"
	access := "pending"
	if ready {
		lifecycle = "ready"
		access = "approved"
	}
	if err = createZone(ctx, tx, in.Zone, lifecycle); err != nil {
		return err
	}
	if err = createCluster(ctx, tx, in.Cluster, lifecycle, true); err != nil {
		return err
	}
	m := in.Membership
	m.LifecycleState = lifecycle
	m.AccessState = access
	if err = createMembership(ctx, tx, m, true); err != nil {
		return err
	}
	if err = createPool(ctx, tx, in.Pool); err != nil {
		return err
	}
	if err = createGroup(ctx, tx, in.Group); err != nil {
		return err
	}
	return nil
}

func (s *Store) CreateZone(ctx context.Context, z api.ZoneInput) error {
	return createZone(ctx, s.DB, z, "ready")
}
func (s *Store) CreateCluster(ctx context.Context, c api.ClusterInput) error {
	return createCluster(ctx, s.DB, c, "ready", false)
}
func (s *Store) CreateNodePool(ctx context.Context, p api.NodePoolInput) error {
	return createPool(ctx, s.DB, p)
}
func (s *Store) CreateNodeGroup(ctx context.Context, g api.NodeGroupInput) error {
	return createGroup(ctx, s.DB, g)
}
func (s *Store) CreateClusterZone(ctx context.Context, m api.ClusterZoneInput) error {
	return createMembership(ctx, s.DB, m, false)
}
func (s *Store) UpdateClusterZone(ctx context.Context, m api.ClusterZoneInput) error {
	if err := normalizeMembership(&m); err != nil {
		return err
	}
	r, err := s.DB.ExecContext(ctx, `UPDATE cluster_zones SET lifecycle_state=$3,access_state=$4 WHERE cluster_id=$1 AND zone_id=$2`, m.ClusterID, m.ZoneID, m.LifecycleState, m.AccessState)
	if err != nil {
		return topologyError(err)
	}
	n, err := r.RowsAffected()
	if err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return err
}
func (s *Store) Host(ctx context.Context, id string) (api.Host, error) {
	var h api.Host
	h.ID = id
	var created, heartbeat sql.NullTime
	err := s.DB.QueryRowContext(
		ctx,
		`SELECT COALESCE(provider,''),COALESCE(providerid,''),COALESCE(crane_zone,zone,''),state,COALESCE(health,'unknown'),createdat,lastseenheartbeat,COALESCE(node_group_id,''),COALESCE(node_pool_id,''),COALESCE(operation_id,''),desired_state_version,observed_state_version,allocatable_vcpu,allocatable_memory_mib,reserved_vcpu,reserved_memory_mib,excess_capacity,protected,subd_endpoint FROM host WHERE id=$1`,
		id,
	).Scan(
		&h.Provider, &h.ProviderID, &h.Zone, &h.State, &h.Health,
		&created, &heartbeat,
		&h.NodeGroupID, &h.NodePoolID, &h.OperationID,
		&h.DesiredStateVersion, &h.ObservedStateVersion,
		&h.Allocatable.VCPU, &h.Allocatable.MemoryMiB,
		&h.Reserved.VCPU, &h.Reserved.MemoryMiB,
		&h.ExcessCapacity,
		&h.Protected, &h.SubdEndpoint,
	)
	h.CreatedAt = created.Time
	h.LastSeenHeartbeat = heartbeat.Time
	if h.NodeGroupID != "" && h.State != api.HostTerminated {
		h.Reserved.Nodes = 1
	}
	return h, err
}

// CreateRemoval persists exclusive drain ownership for a named host. The caller
// must derive clusterID from authentication. Autoscaler deletion lowers target
// with CAS; repair/maintenance preserve it. Credits remain until termination.
// Phase 2 supplies the Kubernetes eviction and provider termination executor.
func (s *Store) CreateRemoval(ctx context.Context, clusterID string, expectedRevision int64, removal api.NodeRemovalOperation) (string, error) {
	if clusterID == "" || removal.NodeGroupID == "" || removal.HostID == "" || removal.NodeName == "" ||
		removal.ProviderID == "" || removal.DrainDeadline.IsZero() {
		return "", errors.New("invalid removal operation")
	}
	if removal.Reason != "autoscaler-delete" && removal.Reason != "repair" && removal.Reason != "maintenance" {
		return "", errors.New("invalid removal reason")
	}
	if removal.PreserveDesiredCapacity != (removal.Reason != "autoscaler-delete") {
		return "", errors.New("invalid desired capacity policy")
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	p, err := loadPolicy(ctx, tx, removal.NodeGroupID)
	if err != nil {
		return "", err
	}
	if p.group.ClusterID != clusterID {
		return "", ErrForbidden
	}
	if p.group.TargetRevision != expectedRevision {
		return "", ErrConflict
	}

	var group, provider string
	err = tx.QueryRowContext(
		ctx,
		`SELECT COALESCE(node_group_id,''),COALESCE(providerid,'') FROM host WHERE id=$1 AND state NOT IN ('TERMINATED','TERMINATING','FAILED') FOR UPDATE`,
		removal.HostID,
	).Scan(&group, &provider)
	if err != nil {
		return "", err
	}
	if group != removal.NodeGroupID || provider != removal.ProviderID {
		return "", ErrForbidden
	}

	revision := p.group.TargetRevision
	if !removal.PreserveDesiredCapacity {
		if p.group.TargetNodes <= p.group.MinNodes {
			return "", ErrLimit
		}
		revision++
		_, err = tx.ExecContext(
			ctx,
			`UPDATE node_groups SET target_nodes=target_nodes-1,target_revision=$2 WHERE id=$1`,
			p.group.ID, revision,
		)
		if err != nil {
			return "", err
		}
	}

	id := uuid.NewString()
	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO operations(id,kind,zone_id,group_id,host_id,target_revision,provider_token,parameters)
		 VALUES($1,'remove_node',$2,$3,$4,$5,$6,'{}')`,
		id, p.group.ZoneID, p.group.ID, removal.HostID, revision, uuid.NewString(),
	)
	if err != nil {
		return "", err
	}

	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO node_removal_operations(operation_id,group_id,host_id,node_name,provider_id,reason,preserve_desired_capacity,drain_deadline)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8)`,
		id, p.group.ID, removal.HostID, removal.NodeName, removal.ProviderID,
		removal.Reason, removal.PreserveDesiredCapacity, removal.DrainDeadline,
	)
	if err != nil {
		return "", err
	}

	if err = tx.Commit(); err != nil {
		return "", err
	}
	return id, nil
}

// BootstrapPhase updates the state machine only under an active fenced lease.
// The final Ready transition requires a validated handoff supplied by the trusted
// layer-zero executor. A host cannot call this boundary to declare readiness.
func (s *Store) BootstrapPhase(ctx context.Context, op api.Operation, next api.ZoneBootstrapPhase, validated bool) error {
	if op.Kind != "create_zone" && op.Kind != "create_cluster" && op.Kind != "attach_zone" {
		return errors.New("not a bootstrap operation")
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var phase api.ZoneBootstrapPhase
	var clusterID sql.NullString
	err = tx.QueryRowContext(
		ctx,
		`SELECT b.phase,b.cluster_id FROM zone_bootstrap_operations b JOIN operations o ON o.id=b.operation_id WHERE o.id=$1 AND o.lease_owner=$2 AND o.fence=$3 AND o.lease_until>clock_timestamp() AND o.state IN ('leased','launching') FOR UPDATE OF o,b`,
		op.ID, op.LeaseOwner, op.Fence,
	).Scan(&phase, &clusterID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrFenced
	}
	if err != nil {
		return err
	}

	expected, ok := bootstrapNext(op.Kind, phase)
	if next != api.ZoneFailed && (!ok || expected != next) {
		return errors.New("invalid bootstrap phase transition")
	}
	if next == api.ZoneReady && !validated {
		return errors.New("bootstrap handoff has not been validated")
	}
	if next == api.ZoneReady && clusterID.Valid {
		if op.Kind == "create_cluster" {
			if _, err = tx.ExecContext(ctx, `UPDATE clusters SET lifecycle_state='ready' WHERE id=$1 AND lifecycle_state='initializing'`, clusterID.String); err != nil {
				return err
			}
		}
		if _, err = tx.ExecContext(ctx, `UPDATE zones SET lifecycle_state='ready' WHERE id=$1 AND lifecycle_state='initializing'`, op.ZoneID); err != nil {
			return err
		}
		r, e := tx.ExecContext(ctx, `UPDATE cluster_zones SET lifecycle_state='ready',access_state='approved' WHERE cluster_id=$1 AND zone_id=$2 AND lifecycle_state='initializing' AND access_state='pending'`, clusterID.String, op.ZoneID)
		if e != nil {
			return e
		}
		count, e := r.RowsAffected()
		if e != nil {
			return e
		}
		if count != 1 {
			return ErrConflict
		}
	}

	_, err = tx.ExecContext(
		ctx,
		`UPDATE zone_bootstrap_operations SET phase=$2 WHERE operation_id=$1`,
		op.ID, next,
	)
	if err != nil {
		return err
	}
	if next == api.ZoneReady || next == api.ZoneFailed {
		state := "failed"
		if next == api.ZoneReady {
			state = "succeeded"
		}
		_, err = tx.ExecContext(
			ctx,
			`UPDATE operations SET state=$2,lease_owner=NULL,lease_until=NULL,updated_at=clock_timestamp() WHERE id=$1`,
			op.ID, state,
		)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Observe records heartbeat outcomes without accepting placement or readiness.
func (s *Store) Observe(ctx context.Context, hostID, observedVersion string, heartbeat time.Time) error {
	_, err := s.DB.ExecContext(
		ctx,
		`UPDATE host SET observed_state_version=$2,lastseenheartbeat=$3 WHERE id=$1 AND (lastseenheartbeat IS NULL OR lastseenheartbeat<=$3)`,
		hostID, observedVersion, heartbeat,
	)
	return err
}
