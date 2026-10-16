package capacity

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/nabutabu/crane-oss/pkg/api"
)

// BootstrapExecutor performs one externally observable layer-zero step. The
// executor owns cloud/Kubernetes/SPIRE clients; this package owns durable
// ordering, leases, and the readiness handoff.
type BootstrapExecutor interface {
	Step(context.Context, api.Operation, api.ZoneBootstrapPhase) error
	Validated(context.Context, api.Operation) (bool, error)
}

type BootstrapController struct {
	Store    *Store
	Executor BootstrapExecutor
	Lease    time.Duration
}

// RunOnce advances at most one phase. Re-running after a crash claims the same
// immutable operation and resumes from the persisted phase.
func (c *BootstrapController) RunOnce(ctx context.Context, zoneID, owner string) error {
	if c.Store == nil || c.Executor == nil || owner == "" {
		return fmt.Errorf("bootstrap controller is not configured")
	}
	d := c.Lease
	if d == 0 {
		d = 2 * time.Minute
	}
	op, err := c.Store.Claim(ctx, zoneID, "create_zone", owner, d)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	phase, err := c.currentPhase(ctx, op.ID)
	if err != nil {
		return err
	}
	next := map[api.ZoneBootstrapPhase]api.ZoneBootstrapPhase{
		api.ZoneRequested:                api.ZoneDatabaseProvisioning,
		api.ZoneDatabaseProvisioning:     api.ZoneDatabaseReady,
		api.ZoneDatabaseReady:            api.ZoneSpireHostProvisioning,
		api.ZoneSpireHostProvisioning:    api.ZoneSpireInstalling,
		api.ZoneSpireInstalling:          api.ZoneSpireValidating,
		api.ZoneSpireValidating:          api.ZoneControlPlaneInitializing,
		api.ZoneControlPlaneInitializing: api.ZoneNetworkInstalling,
		api.ZoneNetworkInstalling:        api.ZoneDominatorInstalling,
		api.ZoneDominatorInstalling:      api.ZoneValidating,
	}
	if phase == api.ZoneValidating {
		ok, e := c.Executor.Validated(ctx, op)
		if e != nil {
			_ = c.Store.BootstrapPhase(ctx, op, api.ZoneFailed, false)
			return e
		}
		return c.Store.BootstrapPhase(ctx, op, api.ZoneReady, ok)
	}
	n, ok := next[phase]
	if !ok {
		return fmt.Errorf("bootstrap operation %s is in terminal phase %s", op.ID, phase)
	}
	if err = c.Executor.Step(ctx, op, n); err != nil {
		_ = c.Store.BootstrapPhase(ctx, op, api.ZoneFailed, false)
		return err
	}
	return c.Store.BootstrapPhase(ctx, op, n, false)
}

func (c *BootstrapController) currentPhase(ctx context.Context, operationID string) (api.ZoneBootstrapPhase, error) {
	var phase api.ZoneBootstrapPhase
	err := c.Store.DB.QueryRowContext(ctx, `SELECT phase FROM zone_bootstrap_operations WHERE operation_id=$1`, operationID).Scan(&phase)
	return phase, err
}
