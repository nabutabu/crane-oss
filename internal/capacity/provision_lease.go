package capacity

import (
	"context"
	"github.com/nabutabu/crane-oss/pkg/api"
)

func (s *Store) CheckProvisionLease(ctx context.Context, op api.Operation) error {
	var allowed bool
	err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM operations o
 JOIN node_groups g ON g.id=o.group_id JOIN cluster_zones m ON m.cluster_id=g.cluster_id AND m.zone_id=g.zone_id
 WHERE o.id=$1 AND o.host_id=$2 AND o.lease_owner=$3 AND o.fence=$4 AND o.lease_until>clock_timestamp()
 AND o.kind='provision' AND o.state='launching' AND m.lifecycle_state='ready' AND m.access_state='approved' AND g.cluster_id=$5)`, op.ID, op.HostID, op.LeaseOwner, op.Fence, op.ClusterID).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrFenced
	}
	return nil
}
