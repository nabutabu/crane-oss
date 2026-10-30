package capacity

import (
	"context"
	"net"
	"net/url"
	"strings"

	"github.com/nabutabu/crane-oss/pkg/api"
)

// SetDominatorRoute records the private, zone-scoped route only after the
// provider has successfully installed the matching workload identity.
func (s *Store) SetDominatorRoute(ctx context.Context, op api.Operation, result api.BootstrapStepResult) error {
	u, err := url.Parse(result.DominatorEndpoint)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Port() != "44000" {
		return ErrInvalid
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		if !ip.IsPrivate() {
			return ErrInvalid
		}
	} else if !strings.HasSuffix(host, ".svc.cluster.local") || !strings.HasPrefix(host, "dominator-") {
		return ErrInvalid
	}
	identity, err := url.Parse(result.DominatorIdentity)
	expectedSuffix := "/dominator/" + op.ZoneID
	if err != nil || op.ZoneID == "" || identity.Scheme != "spiffe" || identity.Host == "" || identity.RawQuery != "" || identity.Fragment != "" || !strings.HasSuffix(identity.Path, expectedSuffix) || identity.Path != expectedSuffix {
		return ErrInvalid
	}
	r, err := s.DB.ExecContext(ctx, `INSERT INTO zone_dominators(zone_id,endpoint,identity)
SELECT b.zone_id,$4,$5 FROM zone_bootstrap_operations b JOIN operations o ON o.id=b.operation_id
WHERE o.id=$1 AND o.kind IN ('create_cluster','attach_zone') AND o.lease_owner=$2 AND o.fence=$3
AND o.lease_until>clock_timestamp() AND o.state IN ('leased','launching') AND b.phase='NetworkInstalling'
ON CONFLICT(zone_id) DO UPDATE SET endpoint=EXCLUDED.endpoint,identity=EXCLUDED.identity
WHERE zone_dominators.endpoint=EXCLUDED.endpoint AND zone_dominators.identity=EXCLUDED.identity`, op.ID, op.LeaseOwner, op.Fence, result.DominatorEndpoint, result.DominatorIdentity)
	return fencedResult(r, err)
}
