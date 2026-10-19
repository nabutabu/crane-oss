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

// SeedFirstZone atomically creates the platform team's initial topology. It is
// intentionally idempotent by durable IDs, making it safe to rerun after a
// lost command response. Records are left ready, but no capacity operation is
// created until a client explicitly changes a target.
func (s *Store) SeedFirstZone(
	ctx context.Context,
	z api.ZoneInput,
	c api.ClusterInput,
	budget api.Resources,
	p api.NodePoolInput,
	g api.NodeGroupInput,
) error {
	return s.seedFirstZone(ctx, z, c, budget, p, g, true)
}

// SeedFirstZonePending initializes topology before services start without
// admitting capacity until the bootstrap's health checks have passed.
func (s *Store) SeedFirstZonePending(ctx context.Context, z api.ZoneInput, c api.ClusterInput, budget api.Resources, p api.NodePoolInput, g api.NodeGroupInput) error {
	return s.seedFirstZone(ctx, z, c, budget, p, g, false)
}

func (s *Store) seedFirstZone(ctx context.Context, z api.ZoneInput, c api.ClusterInput, budget api.Resources, p api.NodePoolInput, g api.NodeGroupInput, ready bool) error {
	if z.ID == "" || c.ID == "" || p.ID == "" || g.ID == "" ||
		c.ZoneID != z.ID || g.ClusterID != c.ID || g.ZoneID != z.ID || g.NodePoolID != p.ID ||
		budget.VCPU < 0 || budget.MemoryMiB < 0 || budget.Nodes < 0 ||
		p.Limits.VCPU < 0 || p.Limits.MemoryMiB < 0 || p.Limits.Nodes < 0 ||
		g.MinNodes < 0 || g.MaxNodes < g.MinNodes ||
		g.PerNode.VCPU <= 0 || g.PerNode.MemoryMiB <= 0 ||
		!json.Valid(g.LaunchParameters) {
		return errors.New("invalid first-zone topology")
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	lifecycle := "ready"
	if !ready {
		lifecycle = "initializing"
	}

	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO zones(id,provider,account_id,region,provider_zone_id,enabled,lifecycle_state)
		 VALUES($1,$2,$3,$4,$5,true,$6)
		 ON CONFLICT (id) DO UPDATE SET
		   provider=EXCLUDED.provider,
		   account_id=EXCLUDED.account_id,
		   region=EXCLUDED.region,
		   provider_zone_id=EXCLUDED.provider_zone_id,
		   lifecycle_state=EXCLUDED.lifecycle_state`,
		z.ID, z.Provider, z.AccountID, z.Region, z.ProviderZoneID, lifecycle,
	)
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO clusters(id,zone_id,api_server_endpoint,ca_cert_hash,kubeadm_version,kubelet_version,container_runtime,cni,lifecycle_state)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)
		 ON CONFLICT (id) DO UPDATE SET
		   zone_id=EXCLUDED.zone_id,
		   api_server_endpoint=EXCLUDED.api_server_endpoint,
		   ca_cert_hash=EXCLUDED.ca_cert_hash,
		   kubeadm_version=EXCLUDED.kubeadm_version,
		   kubelet_version=EXCLUDED.kubelet_version,
		   container_runtime=EXCLUDED.container_runtime,
		   cni=EXCLUDED.cni,
		   lifecycle_state=EXCLUDED.lifecycle_state`,
		c.ID, c.ZoneID, c.APIServerEndpoint, c.CACertHash,
		c.KubeadmVersion, c.KubeletVersion, c.ContainerRuntime, c.CNI, lifecycle,
	)
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO team_budgets(team_id,vcpu,memory_mib,nodes)
		 VALUES($1,$2,$3,$4)
		 ON CONFLICT (team_id) DO UPDATE SET
		   vcpu=EXCLUDED.vcpu,
		   memory_mib=EXCLUDED.memory_mib,
		   nodes=EXCLUDED.nodes`,
		p.TeamID, budget.VCPU, budget.MemoryMiB, budget.Nodes,
	)
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO node_pools(id,team_id,enabled,max_vcpu,max_memory_mib,max_nodes)
		 VALUES($1,$2,true,$3,$4,$5)
		 ON CONFLICT (id) DO UPDATE SET
		   team_id=EXCLUDED.team_id,
		   max_vcpu=EXCLUDED.max_vcpu,
		   max_memory_mib=EXCLUDED.max_memory_mib,
		   max_nodes=EXCLUDED.max_nodes`,
		p.ID, p.TeamID, p.Limits.VCPU, p.Limits.MemoryMiB, p.Limits.Nodes,
	)
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO node_groups(id,cluster_id,zone_id,node_pool_id,capacity_class,provider_zone_id,min_nodes,max_nodes,template_revision,vcpu,memory_mib,launch_parameters,lifecycle_state)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,'active')
		 ON CONFLICT (id) DO UPDATE SET
		   cluster_id=EXCLUDED.cluster_id,
		   zone_id=EXCLUDED.zone_id,
		   node_pool_id=EXCLUDED.node_pool_id,
		   capacity_class=EXCLUDED.capacity_class,
		   provider_zone_id=EXCLUDED.provider_zone_id,
		   min_nodes=EXCLUDED.min_nodes,
		   max_nodes=EXCLUDED.max_nodes,
		   template_revision=EXCLUDED.template_revision,
		   vcpu=EXCLUDED.vcpu,
		   memory_mib=EXCLUDED.memory_mib,
		   launch_parameters=EXCLUDED.launch_parameters`,
		g.ID, g.ClusterID, g.ZoneID, g.NodePoolID, g.CapacityClass, g.ProviderZoneID,
		g.MinNodes, g.MaxNodes, g.TemplateRevision,
		g.PerNode.VCPU, g.PerNode.MemoryMiB, []byte(g.LaunchParameters),
	)
	if err != nil {
		return err
	}

	return tx.Commit()
}

func (s *Store) CreateZone(ctx context.Context, z api.ZoneInput) error {
	if z.ID == "" || z.Provider == "" || z.AccountID == "" || z.Region == "" || z.ProviderZoneID == "" {
		return errors.New("invalid zone")
	}
	_, err := s.DB.ExecContext(
		ctx,
		`INSERT INTO zones(id,provider,account_id,region,provider_zone_id,lifecycle_state)
		 VALUES($1,$2,$3,$4,$5,'ready')
		 ON CONFLICT (id) DO NOTHING`,
		z.ID, z.Provider, z.AccountID, z.Region, z.ProviderZoneID,
	)
	return err
}

func (s *Store) CreateCluster(ctx context.Context, c api.ClusterInput) error {
	if c.ID == "" || c.ZoneID == "" || c.KubeadmVersion == "" || c.KubeletVersion == "" || c.ContainerRuntime == "" || c.CNI != "cilium" {
		return errors.New("invalid cluster")
	}
	_, err := s.DB.ExecContext(
		ctx,
		`INSERT INTO clusters(id,zone_id,api_server_endpoint,ca_cert_hash,kubeadm_version,kubelet_version,container_runtime,cni,lifecycle_state)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,'ready')
		 ON CONFLICT (id) DO NOTHING`,
		c.ID, c.ZoneID, c.APIServerEndpoint, c.CACertHash,
		c.KubeadmVersion, c.KubeletVersion, c.ContainerRuntime, c.CNI,
	)
	return err
}

func (s *Store) CreateNodePool(ctx context.Context, p api.NodePoolInput) error {
	if p.ID == "" || p.TeamID == "" || p.Limits.VCPU < 0 || p.Limits.MemoryMiB < 0 || p.Limits.Nodes < 0 {
		return errors.New("invalid node pool")
	}
	_, err := s.DB.ExecContext(
		ctx,
		`INSERT INTO node_pools(id,team_id,max_vcpu,max_memory_mib,max_nodes)
		 VALUES($1,$2,$3,$4,$5)
		 ON CONFLICT (id) DO NOTHING`,
		p.ID, p.TeamID, p.Limits.VCPU, p.Limits.MemoryMiB, p.Limits.Nodes,
	)
	return err
}

func (s *Store) CreateNodeGroup(ctx context.Context, g api.NodeGroupInput) error {
	if g.ID == "" || g.ClusterID == "" || g.ZoneID == "" || g.NodePoolID == "" || g.TemplateRevision == "" ||
		g.MinNodes < 0 || g.MaxNodes < g.MinNodes ||
		g.PerNode.VCPU <= 0 || g.PerNode.MemoryMiB <= 0 ||
		!json.Valid(g.LaunchParameters) {
		return errors.New("invalid node group")
	}
	_, err := s.DB.ExecContext(
		ctx,
		`INSERT INTO node_groups(id,cluster_id,zone_id,node_pool_id,capacity_class,provider_zone_id,min_nodes,max_nodes,template_revision,vcpu,memory_mib,launch_parameters)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		 ON CONFLICT (id) DO NOTHING`,
		g.ID, g.ClusterID, g.ZoneID, g.NodePoolID, g.CapacityClass, g.ProviderZoneID,
		g.MinNodes, g.MaxNodes, g.TemplateRevision,
		g.PerNode.VCPU, g.PerNode.MemoryMiB, []byte(g.LaunchParameters),
	)
	return err
}

// Host reads the extended durable catalog. Legacy hosts have empty placement;
// only control-plane admission assigns group, pool, zone and operation identity.
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
	if op.Kind != "create_zone" {
		return errors.New("not a bootstrap operation")
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var phase api.ZoneBootstrapPhase
	err = tx.QueryRowContext(
		ctx,
		`SELECT b.phase FROM zone_bootstrap_operations b JOIN operations o ON o.id=b.operation_id WHERE o.id=$1 AND o.lease_owner=$2 AND o.fence=$3 AND o.lease_until>clock_timestamp() AND o.state IN ('leased','launching') FOR UPDATE OF o,b`,
		op.ID, op.LeaseOwner, op.Fence,
	).Scan(&phase)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrFenced
	}
	if err != nil {
		return err
	}

	sequence := map[api.ZoneBootstrapPhase]api.ZoneBootstrapPhase{
		api.ZoneRequested:                api.ZoneDatabaseProvisioning,
		api.ZoneDatabaseProvisioning:     api.ZoneDatabaseReady,
		api.ZoneDatabaseReady:            api.ZoneSpireHostProvisioning,
		api.ZoneSpireHostProvisioning:    api.ZoneSpireInstalling,
		api.ZoneSpireInstalling:          api.ZoneSpireValidating,
		api.ZoneSpireValidating:          api.ZoneControlPlaneInitializing,
		api.ZoneControlPlaneInitializing: api.ZoneNetworkInstalling,
		api.ZoneNetworkInstalling:        api.ZoneDominatorInstalling,
		api.ZoneDominatorInstalling:      api.ZoneValidating,
		api.ZoneValidating:               api.ZoneReady,
	}
	if next != api.ZoneFailed && sequence[phase] != next {
		return errors.New("invalid bootstrap phase transition")
	}
	if next == api.ZoneReady && !validated {
		return errors.New("bootstrap handoff has not been validated")
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
