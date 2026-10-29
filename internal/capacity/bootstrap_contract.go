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
// topology must already exist as pending records before work is submitted.
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
	var valid bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM clusters c JOIN zones z ON z.id=$2 WHERE c.id=$1 AND c.lifecycle_state='requested' AND z.lifecycle_state='requested')`, in.ClusterID, in.ZoneID).Scan(&valid)
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

func safeBootstrapParameters(raw json.RawMessage) bool {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return false
	}
	var walk func(map[string]any) bool
	walk = func(m map[string]any) bool {
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
			if child, ok := v.(map[string]any); ok && !walk(child) {
				return false
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
