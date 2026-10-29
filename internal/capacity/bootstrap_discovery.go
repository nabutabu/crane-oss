package capacity

import (
	"context"
	"net/url"
	"regexp"

	"github.com/nabutabu/crane-oss/pkg/api"
)

var caHashPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// SetClusterDiscovery stores only public kubeadm discovery after a fenced
// provider step. Retries may repeat the same values but cannot change them.
func (s *Store) SetClusterDiscovery(ctx context.Context, op api.Operation, result api.BootstrapStepResult) error {
	endpoint, err := url.Parse(result.APIServerEndpoint)
	if op.Kind != "create_cluster" || err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || !caHashPattern.MatchString(result.CACertHash) {
		return ErrInvalid
	}
	r, err := s.DB.ExecContext(ctx, `UPDATE clusters c SET api_server_endpoint=$4,ca_cert_hash=$5
 FROM zone_bootstrap_operations b JOIN operations o ON o.id=b.operation_id
 WHERE c.id=b.cluster_id AND o.id=$1 AND o.kind='create_cluster'
 AND o.lease_owner=$2 AND o.fence=$3 AND o.lease_until>clock_timestamp()
 AND o.state IN ('leased','launching') AND b.phase='InfrastructureProvisioning'
 AND c.lifecycle_state='initializing'
 AND c.api_server_endpoint IN ('',$4) AND c.ca_cert_hash IN ('',$5)`, op.ID, op.LeaseOwner, op.Fence, result.APIServerEndpoint, result.CACertHash)
	return fencedResult(r, err)
}
