package capacity

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/nabutabu/crane-oss/pkg/api"
)

// AutoscalerDiscovery returns a cluster-scoped, transactionally consistent
// topology and host snapshot. A disabled placement remains visible when it
// owns hosts, so the autoscaler can still remove them.
func (s *Store) AutoscalerDiscovery(ctx context.Context, clusterID string) (api.AutoscalerDiscovery, error) {
	var out api.AutoscalerDiscovery
	if clusterID == "" {
		return out, ErrInvalid
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT g.id,g.cluster_id,g.zone_id,g.node_pool_id,g.template_revision,
 g.min_nodes,g.max_nodes,g.vcpu,g.memory_mib,g.scheduling,
 (g.lifecycle_state='active' AND p.enabled AND z.enabled AND z.lifecycle_state='ready'
  AND c.lifecycle_state='ready' AND m.lifecycle_state='ready' AND m.access_state='approved')
 FROM node_groups g JOIN node_pools p ON p.id=g.node_pool_id
 JOIN zones z ON z.id=g.zone_id JOIN clusters c ON c.id=g.cluster_id
 JOIN cluster_zones m ON m.cluster_id=g.cluster_id AND m.zone_id=g.zone_id
 WHERE g.cluster_id=$1 AND ((g.lifecycle_state='active' AND p.enabled AND z.enabled
  AND z.lifecycle_state='ready' AND c.lifecycle_state='ready' AND m.lifecycle_state='ready'
  AND m.access_state='approved') OR EXISTS
  (SELECT 1 FROM host h WHERE h.node_group_id=g.id AND h.state <> 'TERMINATED'))
 ORDER BY g.id`, clusterID)
	if err != nil {
		return out, err
	}
	out.Groups = []api.AutoscalerGroup{}
	for rows.Next() {
		var g api.AutoscalerGroup
		var raw []byte
		if err = rows.Scan(&g.ID, &g.ClusterID, &g.ZoneID, &g.Pool, &g.TemplateRevision,
			&g.MinNodes, &g.MaxNodes, &g.CPU, &g.MemoryMiB, &raw, &g.LaunchReady); err != nil {
			break
		}
		if err = json.Unmarshal(raw, &g.Scheduling); err != nil {
			break
		}
		g.Nodes = []api.AutoscalerNode{}
		out.Groups = append(out.Groups, g)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return api.AutoscalerDiscovery{}, err
	}
	for i := range out.Groups {
		g := &out.Groups[i]
		nodes, queryErr := tx.QueryContext(ctx, `SELECT h.id,COALESCE(h.providerid,''),h.state,z.provider,z.provider_zone_id
 FROM host h JOIN zones z ON z.id=h.crane_zone
 WHERE h.node_group_id=$1 AND h.state <> 'TERMINATED' ORDER BY h.id`, g.ID)
		if queryErr != nil {
			return api.AutoscalerDiscovery{}, queryErr
		}
		for nodes.Next() {
			var n api.AutoscalerNode
			var provider, zone string
			if err = nodes.Scan(&n.HostID, &n.ProviderID, &n.State, &provider, &zone); err != nil {
				break
			}
			if n.ProviderID != "" {
				if provider == "aws" {
					n.ProviderID = fmt.Sprintf("aws:///%s/%s", zone, n.ProviderID)
				}
				g.Nodes = append(g.Nodes, n)
			}
		}
		if err == nil {
			err = nodes.Err()
		}
		nodes.Close()
		if err != nil {
			return api.AutoscalerDiscovery{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return api.AutoscalerDiscovery{}, err
	}
	return out, nil
}
