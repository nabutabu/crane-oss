package capacity

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/nabutabu/crane-oss/pkg/api"
)

func validateScheduling(t api.SchedulingTemplate, pool string) error {
	if t.InstanceType == "" || t.Region == "" || t.Zone == "" ||
		(t.Architecture != "amd64" && t.Architecture != "arm64") || pool == "" {
		return ErrInvalid
	}
	canonical := map[string]string{
		"kubernetes.io/os": "linux", "kubernetes.io/arch": t.Architecture,
		"topology.kubernetes.io/region": t.Region, "topology.kubernetes.io/zone": t.Zone,
		"platform.crane.io/node-pool": pool, "node.kubernetes.io/instance-type": t.InstanceType,
	}
	for k, v := range t.Labels {
		if strings.TrimSpace(k) == "" || v == "" {
			return ErrInvalid
		}
		if expected, ok := canonical[k]; ok && expected != v {
			return ErrInvalid
		}
	}
	for _, taint := range t.Taints {
		if taint.Key == "" || (taint.Effect != "NoSchedule" && taint.Effect != "NoExecute" && taint.Effect != "PreferNoSchedule") {
			return ErrInvalid
		}
	}
	return nil
}

// UpdateNodeGroupScheduling changes only the operator-owned template revision
// and scheduling view. An exact repeated request is idempotent.
func (s *Store) UpdateNodeGroupScheduling(ctx context.Context, id, expected, revision string, t api.SchedulingTemplate, launch json.RawMessage) error {
	if id == "" || expected == "" || revision == "" || !json.Valid(launch) {
		return ErrInvalid
	}
	data, err := json.Marshal(t)
	if err != nil {
		return ErrInvalid
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var pool, current string
	var same bool
	err = tx.QueryRowContext(ctx, `SELECT node_pool_id,template_revision,(scheduling=$2::jsonb AND launch_parameters=$3::jsonb) FROM node_groups WHERE id=$1 FOR UPDATE`, id, data, []byte(launch)).Scan(&pool, &current, &same)
	if errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err != nil {
		return err
	}
	if err = validateScheduling(t, pool); err != nil {
		return err
	}
	if current == revision {
		if !same {
			return ErrConflict
		}
		return tx.Commit()
	}
	if current != expected {
		return ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `UPDATE node_groups SET template_revision=$2,scheduling=$3,launch_parameters=$4 WHERE id=$1`, id, revision, data, []byte(launch)); err != nil {
		return err
	}
	return tx.Commit()
}
