package capacity

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/nabutabu/crane-oss/pkg/api"
)

// LoadAttachmentSource resolves the trusted active group named by immutable
// intent. Provider configuration is still validated by the selected provider.
func (s *Store) LoadAttachmentSource(ctx context.Context, op api.Operation) (api.AttachmentIntent, api.NodeGroup, error) {
	var intent api.AttachmentIntent
	var source api.NodeGroup
	if op.Kind != "attach_zone" || op.ClusterID == "" || json.Unmarshal(op.Parameters, &intent) != nil ||
		intent.SourceGroupID == "" || intent.Zone.ID != op.ZoneID || intent.Membership.ClusterID != op.ClusterID {
		return intent, source, ErrInvalid
	}
	var scheduling []byte
	err := s.DB.QueryRowContext(ctx, `SELECT g.id,g.cluster_id,g.zone_id,g.node_pool_id,g.capacity_class,g.provider_zone_id,
 g.template_revision,g.lifecycle_state,g.vcpu,g.memory_mib,g.launch_parameters,g.scheduling
 FROM node_groups g JOIN zones z ON z.id=g.zone_id
 JOIN cluster_zones m ON m.cluster_id=g.cluster_id AND m.zone_id=g.zone_id
 WHERE g.id=$1 AND g.cluster_id=$2 AND g.lifecycle_state='active'
 AND z.lifecycle_state='ready' AND m.lifecycle_state='ready' AND m.access_state='approved'`,
		intent.SourceGroupID, op.ClusterID).Scan(&source.ID, &source.ClusterID, &source.ZoneID,
		&source.NodePoolID, &source.CapacityClass, &source.ProviderZoneID,
		&source.TemplateRevision, &source.LifecycleState, &source.PerNode.VCPU,
		&source.PerNode.MemoryMiB, &source.LaunchParameters, &scheduling)
	if errors.Is(err, sql.ErrNoRows) {
		return intent, source, ErrInactive
	}
	if err != nil {
		return intent, source, err
	}
	if json.Unmarshal(scheduling, &source.Scheduling) != nil {
		return intent, source, ErrInvalid
	}
	return intent, source, nil
}
