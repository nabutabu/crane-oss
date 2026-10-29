package capacity

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
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
		var intent struct {
			Zone       api.ZoneInput        `json:"zone"`
			Membership api.ClusterZoneInput `json:"membership"`
			Pool       api.NodePoolInput    `json:"pool"`
			Group      api.NodeGroupInput   `json:"group"`
		}
		if json.Unmarshal(in.Parameters, &intent) != nil || intent.Zone.ID != in.ZoneID || intent.Membership.ClusterID != in.ClusterID || intent.Membership.ZoneID != in.ZoneID || intent.Group.ClusterID != in.ClusterID || intent.Group.ZoneID != in.ZoneID || intent.Group.NodePoolID != intent.Pool.ID {
			return "", ErrInvalid
		}
		if err := seedAttachment(ctx, tx, intent.Zone, intent.Membership, intent.Pool, intent.Group); err != nil {
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
	if in.Kind == "attach_zone" {
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM clusters WHERE id=$1 AND lifecycle_state='ready')`, in.ClusterID).Scan(&valid)
		if err != nil {
			return "", err
		}
		if !valid {
			return "", ErrInvalid
		}
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

func seedAttachment(ctx context.Context, tx *sql.Tx, zone api.ZoneInput, membership api.ClusterZoneInput, pool api.NodePoolInput, group api.NodeGroupInput) error {
	var ready bool
	if err := tx.QueryRowContext(ctx, `SELECT lifecycle_state='ready' FROM clusters WHERE id=$1`, membership.ClusterID).Scan(&ready); err != nil {
		return err
	}
	if !ready {
		return ErrInvalid
	}
	if err := createZone(ctx, tx, zone, "initializing"); err != nil {
		return err
	}
	membership.LifecycleState, membership.AccessState = "initializing", "pending"
	if err := createMembership(ctx, tx, membership, true); err != nil {
		return err
	}
	if err := createPool(ctx, tx, pool); err != nil {
		return err
	}
	if err := createGroup(ctx, tx, group); err != nil {
		return err
	}
	return nil
}

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
