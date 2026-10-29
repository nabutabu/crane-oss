package capacity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/nabutabu/crane-oss/pkg/api"
)

// ReleaseBootstrapLease makes a completed nonterminal phase immediately
// claimable. The fence prevents a stale controller from releasing a new lease.
func (s *Store) ReleaseBootstrapLease(ctx context.Context, op api.Operation) error {
	if op.Kind != "create_cluster" && op.Kind != "attach_zone" && op.Kind != "create_zone" {
		return ErrForbidden
	}
	r, err := s.DB.ExecContext(ctx, `UPDATE operations SET state='pending',lease_owner=NULL,lease_until=NULL,updated_at=clock_timestamp()
 WHERE id=$1 AND lease_owner=$2 AND fence=$3 AND lease_until>clock_timestamp() AND state IN ('leased','launching')`, op.ID, op.LeaseOwner, op.Fence)
	return fencedResult(r, err)
}

// NextBootstrapZone returns one runnable zone; expired leases are recoverable
// after a worker restart. This read does not claim work.
func (s *Store) NextBootstrapZone(ctx context.Context) (string, error) {
	var zoneID string
	err := s.DB.QueryRowContext(ctx, `SELECT o.zone_id FROM operations o JOIN zone_bootstrap_operations b ON b.operation_id=o.id
 WHERE o.kind IN ('create_cluster','attach_zone') AND b.phase NOT IN ('Ready','Failed')
 AND (o.state='pending' OR (o.state IN ('leased','launching') AND o.lease_until<=clock_timestamp()))
 ORDER BY o.created_at,o.id LIMIT 1`).Scan(&zoneID)
	return zoneID, err
}

// Run polls durable work until cancellation. Only configured layer-zero
// executors should be passed to this loop; errors are sanitized in logs.
func (c *BootstrapController) Run(ctx context.Context, owner string, interval time.Duration, logger *slog.Logger) error {
	if c.Store == nil || c.Executor == nil || owner == "" || interval <= 0 {
		return fmt.Errorf("bootstrap worker is not configured")
	}
	if logger == nil {
		logger = slog.Default()
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		zoneID, err := c.Store.NextBootstrapZone(ctx)
		if err == nil {
			if stepErr := c.RunOnce(ctx, zoneID, owner); stepErr != nil && ctx.Err() == nil {
				logger.WarnContext(ctx, "bootstrap phase failed", "zone_id", zoneID)
			}
		} else if !errors.Is(err, sql.ErrNoRows) && ctx.Err() == nil {
			logger.WarnContext(ctx, "bootstrap work lookup failed")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
