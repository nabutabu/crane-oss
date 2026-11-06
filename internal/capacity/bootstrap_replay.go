package capacity

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/nabutabu/crane-oss/pkg/api"
)

// LookupBootstrapReplay allows an exact HTTP retry to return its durable ID
// before repeating fallible provider discovery. CreateBootstrap still resolves
// races and enforces the same equality inside its transaction.
func (s *Store) LookupBootstrapReplay(ctx context.Context, in api.BootstrapRequest) (string, error) {
	var id, kind, clusterID, zoneID string
	var previous []byte
	err := s.DB.QueryRowContext(ctx, `SELECT o.id,o.kind,b.cluster_id,o.zone_id,o.parameters
 FROM zone_bootstrap_operations b JOIN operations o ON o.id=b.operation_id
 WHERE b.request_id=$1`, in.RequestID).Scan(&id, &kind, &clusterID, &zoneID, &previous)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var a, b any
	if json.Unmarshal(previous, &a) != nil || json.Unmarshal(in.Parameters, &b) != nil ||
		kind != in.Kind || clusterID != in.ClusterID || zoneID != in.ZoneID || !reflect.DeepEqual(a, b) {
		return "", ErrReplayMismatch
	}
	return id, nil
}
