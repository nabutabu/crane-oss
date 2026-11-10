package capacity

import (
	"context"
	"encoding/json"

	"github.com/nabutabu/crane-oss/pkg/api"
)

// PinAttachmentTemplate records the provider-resolved launch template while
// the group is still inactive. Retrying the same step preserves the pin.
func (s *Store) PinAttachmentTemplate(ctx context.Context, op api.Operation, result api.BootstrapStepResult) error {
	if op.Kind != "attach_zone" || result.TemplateRevision == "" || !safeBootstrapParameters(result.LaunchParameters) ||
		result.Scheduling.InstanceType == "" || result.Scheduling.Region == "" || result.Scheduling.Zone == "" {
		return ErrInvalid
	}
	var intent api.AttachmentIntent
	if json.Unmarshal(op.Parameters, &intent) != nil || intent.Group.NormalizeID() != nil ||
		intent.Zone.ID != op.ZoneID || result.Scheduling.Region != intent.Zone.Region ||
		result.Scheduling.Zone != intent.Zone.ProviderZoneID || validateScheduling(result.Scheduling, intent.Pool.ID) != nil {
		return ErrInvalid
	}
	scheduling, err := json.Marshal(result.Scheduling)
	if err != nil {
		return ErrInvalid
	}
	r, err := s.DB.ExecContext(ctx, `UPDATE node_groups g SET launch_parameters=$4,template_revision=$5,scheduling=$6
 FROM zone_bootstrap_operations b JOIN operations o ON o.id=b.operation_id
 WHERE o.id=$1 AND o.kind='attach_zone' AND o.lease_owner=$2 AND o.fence=$3
 AND o.lease_until>clock_timestamp() AND o.state IN ('leased','launching')
 AND b.phase='Requested' AND b.zone_id=g.zone_id AND b.cluster_id=g.cluster_id
 AND g.id=$7 AND g.lifecycle_state='initializing' AND g.target_nodes=0 AND g.min_nodes=0
 AND (g.launch_parameters='{}'::jsonb OR
      (g.launch_parameters=$4::jsonb AND g.template_revision=$5 AND g.scheduling=$6::jsonb))`,
		op.ID, op.LeaseOwner, op.Fence, []byte(result.LaunchParameters), result.TemplateRevision, scheduling, intent.Group.ID)
	return fencedResult(r, err)
}
