package capacity

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/nabutabu/crane-oss/pkg/api"
)

// CreateAutoscalerRemovals lowers the target and reserves exclusive ownership
// of all named hosts in one transaction. Existing operation parameters hold the
// immutable handler request for replay; no schema upgrade is needed. The group
// row lock serializes both replay lookup and insertion, as in SetTarget.
func (s *Store) CreateAutoscalerRemovals(ctx context.Context, clusterID string, req api.NodeRemovalRequest) error {
	if clusterID == "" || req.NodeGroupID == "" || req.RequestID == "" || req.ExpectedTargetRevision < 0 || len(req.Nodes) == 0 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, node := range req.Nodes {
		if node.HostID == "" || node.ProviderID == "" || node.NodeName == "" || seen[node.HostID] {
			return ErrInvalid
		}
		seen[node.HostID] = true
	}
	requestJSON, err := json.Marshal(req)
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := loadPolicy(ctx, tx, req.NodeGroupID)
	if err != nil {
		return err
	}
	if p.group.ClusterID != clusterID {
		return ErrForbidden
	}
	var same bool
	err = tx.QueryRowContext(ctx, `SELECT parameters->'request'=$3::jsonb FROM operations
		WHERE group_id=$1 AND kind='remove_node' AND parameters->'request'->>'request_id'=$2 LIMIT 1`,
		req.NodeGroupID, req.RequestID, requestJSON).Scan(&same)
	if err == nil {
		if !same {
			return ErrReplayMismatch
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if p.group.TargetRevision != req.ExpectedTargetRevision {
		return ErrConflict
	}
	if p.group.TargetNodes-int64(len(req.Nodes)) < p.group.MinNodes || p.group.TargetRevision == math.MaxInt64 {
		return ErrLimit
	}
	// Validate every identity and existing removal before changing any target.
	providerIDs := make([]string, len(req.Nodes))
	for i, node := range req.Nodes {
		var group, provider, providerID, zone, state string
		err = tx.QueryRowContext(ctx, `SELECT COALESCE(h.node_group_id,''),COALESCE(h.provider,''),COALESCE(h.providerid,''),z.provider_zone_id,h.state
			FROM host h JOIN zones z ON z.id=h.crane_zone WHERE h.id=$1 FOR UPDATE OF h`, node.HostID).
			Scan(&group, &provider, &providerID, &zone, &state)
		if err != nil {
			return err
		}
		identity := providerID
		if provider == "aws" {
			identity = "aws:///" + zone + "/" + providerID
		}
		if group != req.NodeGroupID || providerID == "" || identity != node.ProviderID {
			return ErrForbidden
		}
		if state == "TERMINATED" || state == "TERMINATING" || state == "FAILED" {
			return ErrFulfilled
		}
		var active bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM node_removal_operations WHERE host_id=$1 AND completed_at IS NULL)`, node.HostID).Scan(&active); err != nil {
			return err
		}
		if active {
			return ErrConflict
		}
		providerIDs[i] = providerID
	}
	revision := p.group.TargetRevision + 1
	parameters, err := json.Marshal(struct {
		Request api.NodeRemovalRequest `json:"request"`
	}{req})
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE node_groups SET target_nodes=target_nodes-$2,target_revision=$3 WHERE id=$1`, req.NodeGroupID, len(req.Nodes), revision); err != nil {
		return err
	}
	deadline := time.Now().Add(30 * time.Minute)
	for i, node := range req.Nodes {
		id := uuid.NewString()
		if _, err = tx.ExecContext(ctx, `INSERT INTO operations(id,kind,zone_id,group_id,host_id,target_revision,provider_token,parameters)
			VALUES($1,'remove_node',$2,$3,$4,$5,$6,$7)`, id, p.group.ZoneID, req.NodeGroupID, node.HostID, revision, uuid.NewString(), parameters); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO node_removal_operations(operation_id,group_id,host_id,node_name,provider_id,reason,preserve_desired_capacity,drain_deadline)
			VALUES($1,$2,$3,$4,$5,'autoscaler-delete',false,$6)`, id, req.NodeGroupID, node.HostID, node.NodeName, providerIDs[i], deadline); err != nil {
			return err
		}
	}
	return tx.Commit()
}
