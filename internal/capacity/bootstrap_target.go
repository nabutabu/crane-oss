package capacity

import (
	"context"
	"database/sql"

	"github.com/nabutabu/crane-oss/pkg/api"
)

// LoadBootstrapTarget reads cluster discovery through the operation's durable
// relationship. Request parameters cannot override this trusted runtime data.
func (s *Store) LoadBootstrapTarget(ctx context.Context, op *api.Operation) error {
	if op == nil || (op.Kind != "create_cluster" && op.Kind != "attach_zone") {
		return ErrInvalid
	}
	err := s.DB.QueryRowContext(ctx, `SELECT c.id,c.api_server_endpoint,c.ca_cert_hash FROM zone_bootstrap_operations b
 JOIN clusters c ON c.id=b.cluster_id JOIN operations o ON o.id=b.operation_id
 WHERE o.id=$1 AND o.kind=$2 AND o.zone_id=$3`, op.ID, op.Kind, op.ZoneID).Scan(&op.ClusterID, &op.APIServerEndpoint, &op.CACertHash)
	if err == sql.ErrNoRows {
		return ErrInvalid
	}
	if err == nil && op.Kind == "attach_zone" && (op.APIServerEndpoint == "" || !caHashPattern.MatchString(op.CACertHash)) {
		return ErrInvalid
	}
	return err
}
