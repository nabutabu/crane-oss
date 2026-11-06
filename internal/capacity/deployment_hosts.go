package capacity

import (
	"context"
	"database/sql"
	"errors"
)

type DeploymentHosts struct {
	SpireInstanceID, PlatformInstanceID string
}

// LoadDeploymentHosts resolves protected provider identities from the catalog.
// Attachment intent never supplies either authority.
func (s *Store) LoadDeploymentHosts(ctx context.Context, deployment DeploymentRuntime) (DeploymentHosts, error) {
	var hosts DeploymentHosts
	if deployment.ClusterID == "" || deployment.SpireHostID == "" || deployment.PlatformHostID == "" {
		return hosts, ErrInvalid
	}
	err := s.DB.QueryRowContext(ctx, `SELECT spire.providerid,platform.providerid
 FROM deployment_runtime d
 JOIN host spire ON spire.id=d.spire_host_id AND spire.role='spire' AND spire.protected
   AND spire.provider=d.provider AND spire.providerid IS NOT NULL AND spire.providerid<>''
 JOIN host platform ON platform.id=d.platform_host_id AND platform.role='platform' AND platform.protected
   AND platform.provider=d.provider AND platform.providerid IS NOT NULL AND platform.providerid<>''
 JOIN cluster_zones sm ON sm.cluster_id=d.cluster_id AND sm.zone_id=spire.zone
 JOIN cluster_zones pm ON pm.cluster_id=d.cluster_id AND pm.zone_id=platform.zone
 WHERE d.cluster_id=$1 AND d.deployment_id=$2 AND d.spire_host_id=$3 AND d.platform_host_id=$4
 AND sm.lifecycle_state='ready' AND sm.access_state='approved'
 AND pm.lifecycle_state='ready' AND pm.access_state='approved'`,
		deployment.ClusterID, deployment.DeploymentID, deployment.SpireHostID, deployment.PlatformHostID).
		Scan(&hosts.SpireInstanceID, &hosts.PlatformInstanceID)
	if errors.Is(err, sql.ErrNoRows) {
		return DeploymentHosts{}, ErrInactive
	}
	if err != nil {
		return DeploymentHosts{}, err
	}
	return hosts, nil
}
