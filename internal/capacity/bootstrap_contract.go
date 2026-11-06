package capacity

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/nabutabu/crane-oss/pkg/api"
)

type BootstrapStatus struct {
	OperationID string                 `json:"operation_id"`
	Kind        string                 `json:"kind"`
	ClusterID   string                 `json:"cluster_id"`
	ZoneID      string                 `json:"zone_id"`
	State       string                 `json:"state"`
	Phase       api.ZoneBootstrapPhase `json:"phase"`
}

// CreateBootstrap stores an operator's immutable, secret-free intent. The
// topology is seeded as pending before the operation becomes visible.
func (s *Store) CreateBootstrap(ctx context.Context, in api.BootstrapRequest) (string, error) {
	if (in.Kind != "create_cluster" && in.Kind != "attach_zone") || in.RequestID == "" ||
		in.ClusterID == "" || in.ZoneID == "" || !safeBootstrapParameters(in.Parameters) {
		return "", ErrInvalid
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	// Serialize requests for the same cluster so retries and competing
	// attachments cannot race the topology seed or active-operation check.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 724163041))`, in.ClusterID); err != nil {
		return "", err
	}
	var id, kind, clusterID, zoneID string
	var previous []byte
	err = tx.QueryRowContext(ctx, `SELECT o.id,o.kind,b.cluster_id,o.zone_id,o.parameters FROM zone_bootstrap_operations b JOIN operations o ON o.id=b.operation_id WHERE b.request_id=$1`, in.RequestID).Scan(&id, &kind, &clusterID, &zoneID, &previous)
	if err == nil {
		var a, b any
		_ = json.Unmarshal(previous, &a)
		_ = json.Unmarshal(in.Parameters, &b)
		if kind != in.Kind || clusterID != in.ClusterID || zoneID != in.ZoneID || !reflect.DeepEqual(a, b) {
			return "", ErrReplayMismatch
		}
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	var active bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(
	 SELECT 1 FROM zone_bootstrap_operations b JOIN operations o ON o.id=b.operation_id
	 WHERE b.cluster_id=$1 AND b.phase NOT IN ('Ready','Failed')
	   AND (o.kind='create_cluster' OR (o.kind='attach_zone' AND o.zone_id=$2)))`, in.ClusterID, in.ZoneID).Scan(&active)
	if err != nil {
		return "", err
	}
	if active {
		return "", ErrConflict
	}
	if in.Kind == "create_cluster" {
		var intent struct {
			FirstZone api.FirstZoneInput `json:"first_zone"`
		}
		if json.Unmarshal(in.Parameters, &intent) != nil || intent.FirstZone.Cluster.ID != in.ClusterID || intent.FirstZone.Zone.ID != in.ZoneID {
			return "", ErrInvalid
		}
		if err := seedFirstZoneTx(ctx, tx, intent.FirstZone, false); err != nil {
			return "", err
		}
	} else {
		var intent api.AttachmentIntent
		d := json.NewDecoder(bytes.NewReader(in.Parameters))
		d.DisallowUnknownFields()
		if d.Decode(&intent) != nil || intent.Zone.ID != in.ZoneID ||
			intent.Membership.ClusterID != in.ClusterID || intent.Membership.ZoneID != in.ZoneID ||
			intent.Group.ClusterID != in.ClusterID || intent.Group.ZoneID != in.ZoneID ||
			intent.Group.NodePoolID != intent.Pool.ID || intent.Group.MinNodes != 0 {
			return "", ErrInvalid
		}
		if err := seedAttachmentTx(ctx, tx, intent); err != nil {
			return "", err
		}
	}

	var valid bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM clusters c JOIN zones z ON z.id=$2 WHERE c.id=$1 AND c.lifecycle_state='initializing' AND z.lifecycle_state='initializing')`, in.ClusterID, in.ZoneID).Scan(&valid)
	if err != nil {
		return "", err
	}
	if !valid && in.Kind == "create_cluster" {
		return "", ErrInvalid
	}

	id = uuid.NewString()
	_, err = tx.ExecContext(ctx, `INSERT INTO operations(id,kind,zone_id,provider_token,parameters) VALUES($1,$2,$3,$4,$5)`, id, in.Kind, in.ZoneID, uuid.NewString(), []byte(in.Parameters))
	if err != nil {
		return "", err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO zone_bootstrap_operations(operation_id,zone_id,cluster_id,request_id) VALUES($1,$2,$3,$4)`, id, in.ZoneID, in.ClusterID, in.RequestID)
	if err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return id, nil
}

func seedAttachmentTx(ctx context.Context, tx *sql.Tx, in api.AttachmentIntent) error {
	if len(in.Zone.ID) > 48 || !attachmentZoneIDRE.MatchString(in.Zone.ID) {
		return ErrInvalid
	}
	var providerConfig map[string]json.RawMessage
	if json.Unmarshal(in.ProviderConfig, &providerConfig) != nil || providerConfig == nil {
		return ErrInvalid
	}
	var provider, accountID, region string
	err := tx.QueryRowContext(ctx, `SELECT d.provider,d.account_id,d.region
 FROM clusters c JOIN deployment_runtime d ON d.cluster_id=c.id
 WHERE c.id=$1 AND c.lifecycle_state='ready'`, in.Membership.ClusterID).
		Scan(&provider, &accountID, &region)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrInvalid
	}
	if err != nil {
		return err
	}
	if in.Zone.Provider != provider || in.Zone.AccountID != accountID || in.Zone.Region != region ||
		in.Group.ProviderZoneID != in.Zone.ProviderZoneID || in.Group.Naming.Provider != provider ||
		in.Group.Naming.Region != region || in.Group.NormalizeID() != nil || in.SourceGroupID == "" {
		return ErrInvalid
	}
	var sourceReady bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM node_groups g
 JOIN zones z ON z.id=g.zone_id JOIN cluster_zones m ON m.zone_id=g.zone_id AND m.cluster_id=g.cluster_id
 WHERE g.id=$1 AND g.cluster_id=$2 AND g.lifecycle_state='active' AND g.launch_parameters!='{}'::jsonb
 AND g.capacity_class=$3 AND g.vcpu=$4 AND g.memory_mib=$5 AND z.lifecycle_state='ready'
 AND m.lifecycle_state='ready' AND m.access_state='approved')`,
		in.SourceGroupID, in.Membership.ClusterID, in.Group.CapacityClass, in.Group.PerNode.VCPU, in.Group.PerNode.MemoryMiB).Scan(&sourceReady)
	if err != nil {
		return err
	}
	if !sourceReady {
		return ErrInvalid
	}
	if err := createZone(ctx, tx, in.Zone, "initializing"); err != nil {
		return err
	}
	in.Membership.LifecycleState, in.Membership.AccessState = "initializing", "pending"
	if err := createMembership(ctx, tx, in.Membership, false); err != nil {
		return err
	}
	if err := createPool(ctx, tx, in.Pool); err != nil {
		return err
	}
	// The provider resolves the complete launch template before activation.
	if len(in.Group.LaunchParameters) != 0 && string(in.Group.LaunchParameters) != "{}" {
		return ErrInvalid
	}
	in.Group.LaunchParameters = json.RawMessage(`{}`)
	if err := createGroup(ctx, tx, in.Group); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE node_groups SET lifecycle_state='initializing'
 WHERE id=$1 AND cluster_id=$2 AND zone_id=$3 AND target_nodes=0 AND min_nodes=0`,
		in.Group.ID, in.Membership.ClusterID, in.Zone.ID)
	return err
}

var attachmentZoneIDRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

func safeBootstrapParameters(raw json.RawMessage) bool {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return false
	}
	var walk func(any) bool
	walk = func(value any) bool {
		switch m := value.(type) {
		case []any:
			for _, item := range m {
				if !walk(item) {
					return false
				}
			}
		case map[string]any:
			for key, v := range m {
				name := strings.ToLower(key)
				if strings.HasSuffix(name, "_arn") || strings.HasSuffix(name, "_ref") || strings.HasSuffix(name, "_reference") {
					// References must be opaque strings. A nested object here would
					// otherwise bypass the recursive credential check.
					if _, ok := v.(string); !ok {
						return false
					}
					continue
				}
				for _, forbidden := range []string{"password", "secret", "token", "credential", "private_key"} {
					if strings.Contains(name, forbidden) {
						return false
					}
				}
				if !walk(v) {
					return false
				}
			}
		}
		return true
	}
	return walk(obj)
}

func (s *Store) BootstrapStatus(ctx context.Context, id string) (BootstrapStatus, error) {
	var out BootstrapStatus
	err := s.DB.QueryRowContext(ctx, `SELECT o.id,o.kind,b.cluster_id,o.zone_id,o.state,b.phase FROM zone_bootstrap_operations b JOIN operations o ON o.id=b.operation_id WHERE o.id=$1`, id).Scan(&out.OperationID, &out.Kind, &out.ClusterID, &out.ZoneID, &out.State, &out.Phase)
	return out, err
}
