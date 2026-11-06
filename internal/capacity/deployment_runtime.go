package capacity

import (
	"context"
	"database/sql"
	"errors"

	"github.com/nabutabu/crane-oss/pkg/api"
)

// DeploymentRuntime holds non-secret, deployment-wide references trusted by
// attachment and worker provisioning. It is never derived from an attach request.
type DeploymentRuntime struct {
	ClusterID, DeploymentID, Provider, AccountID, Region, VPCID    string
	SpireHostID, PlatformHostID, DominatorImage, RuntimeSecretName string
	TrustDomain, PodCIDR                                           string
}

// RecordDeploymentRuntime is repeatable after a lost bootstrap response. A
// retry may confirm the original record, but cannot change shared authority.
func (s *Store) RecordDeploymentRuntime(ctx context.Context, op api.Operation, in DeploymentRuntime) error {
	if op.Kind != "create_cluster" || op.ID == "" || in.ClusterID == "" || in.DeploymentID == "" ||
		in.Provider == "" || in.AccountID == "" || in.Region == "" || in.VPCID == "" ||
		in.SpireHostID == "" || in.PlatformHostID == "" || in.DominatorImage == "" ||
		in.RuntimeSecretName == "" || in.TrustDomain == "" || in.PodCIDR == "" {
		return ErrInvalid
	}
	r, err := s.DB.ExecContext(ctx, `INSERT INTO deployment_runtime
 (cluster_id,deployment_id,provider,account_id,region,vpc_id,spire_host_id,platform_host_id,dominator_image,runtime_secret_name,trust_domain,pod_cidr)
 SELECT b.cluster_id,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14
 FROM zone_bootstrap_operations b JOIN operations o ON o.id=b.operation_id
 WHERE o.id=$1 AND o.kind='create_cluster' AND b.cluster_id=$15
 AND o.lease_owner=$2 AND o.fence=$3 AND o.lease_until>clock_timestamp()
 AND o.state IN ('leased','launching')
 ON CONFLICT(cluster_id) DO UPDATE SET cluster_id=EXCLUDED.cluster_id
 WHERE deployment_runtime.deployment_id=EXCLUDED.deployment_id
 AND deployment_runtime.provider=EXCLUDED.provider
 AND deployment_runtime.account_id=EXCLUDED.account_id
 AND deployment_runtime.region=EXCLUDED.region
 AND deployment_runtime.vpc_id=EXCLUDED.vpc_id
 AND deployment_runtime.spire_host_id=EXCLUDED.spire_host_id
 AND deployment_runtime.platform_host_id=EXCLUDED.platform_host_id
 AND deployment_runtime.dominator_image=EXCLUDED.dominator_image
 AND deployment_runtime.runtime_secret_name=EXCLUDED.runtime_secret_name
 AND deployment_runtime.trust_domain=EXCLUDED.trust_domain
 AND deployment_runtime.pod_cidr=EXCLUDED.pod_cidr`,
		op.ID, op.LeaseOwner, op.Fence, in.DeploymentID, in.Provider, in.AccountID,
		in.Region, in.VPCID, in.SpireHostID, in.PlatformHostID, in.DominatorImage,
		in.RuntimeSecretName, in.TrustDomain, in.PodCIDR, in.ClusterID)
	return fencedResult(r, err)
}

func (s *Store) LoadDeploymentRuntime(ctx context.Context, clusterID string) (DeploymentRuntime, error) {
	var out DeploymentRuntime
	err := s.DB.QueryRowContext(ctx, `SELECT cluster_id,deployment_id,provider,account_id,region,vpc_id,
 spire_host_id,platform_host_id,dominator_image,runtime_secret_name,trust_domain,pod_cidr
 FROM deployment_runtime WHERE cluster_id=$1`, clusterID).Scan(&out.ClusterID, &out.DeploymentID,
		&out.Provider, &out.AccountID, &out.Region, &out.VPCID, &out.SpireHostID,
		&out.PlatformHostID, &out.DominatorImage, &out.RuntimeSecretName,
		&out.TrustDomain, &out.PodCIDR)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrInactive
	}
	return out, err
}
