package capacity

import (
	"context"
	"database/sql"
	"errors"

	"github.com/nabutabu/crane-oss/pkg/api"
)

func (s *Store) LoadAttachmentRoute(ctx context.Context, op api.Operation) (api.BootstrapStepResult, error) {
	var route api.BootstrapStepResult
	if op.Kind != "attach_zone" || op.ZoneID == "" || op.ClusterID == "" {
		return route, ErrInvalid
	}
	err := s.DB.QueryRowContext(ctx, `SELECT d.endpoint,d.identity FROM zone_dominators d
 JOIN zone_bootstrap_operations b ON b.zone_id=d.zone_id
 JOIN operations o ON o.id=b.operation_id
 WHERE o.id=$1 AND o.kind='attach_zone' AND b.cluster_id=$2 AND b.zone_id=$3`,
		op.ID, op.ClusterID, op.ZoneID).Scan(&route.DominatorEndpoint, &route.DominatorIdentity)
	if errors.Is(err, sql.ErrNoRows) {
		return route, ErrInactive
	}
	return route, err
}
