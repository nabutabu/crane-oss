package capacity

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/nabutabu/crane-oss/pkg/api"
)

func validateScheduling(t api.SchedulingTemplate, pool string) error {
	if api.ValidateScheduling(t, pool) != nil {
		return ErrInvalid
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
	var pool, current, region, zone string
	var same bool
	err = tx.QueryRowContext(ctx, `SELECT g.node_pool_id,g.template_revision,(g.scheduling=$2::jsonb AND g.launch_parameters=$3::jsonb),z.kubernetes_region,z.kubernetes_zone FROM node_groups g JOIN zones z ON z.id=g.zone_id WHERE g.id=$1 FOR UPDATE OF g`, id, data, []byte(launch)).Scan(&pool, &current, &same, &region, &zone)
	if errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err != nil {
		return err
	}
	if err = validateScheduling(t, pool); err != nil {
		return err
	}
	if t.Region != region || t.Zone != zone {
		return ErrInvalid
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
