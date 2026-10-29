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
	Step(context.Context, api.Operation, api.ZoneBootstrapPhase) (api.BootstrapStepResult, error)
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
	var op api.Operation
	var err error
	for _, kind := range []string{"create_cluster", "attach_zone"} {
		op, err = c.Store.Claim(ctx, zoneID, kind, owner, d)
		if err != sql.ErrNoRows {
			break
		}
	}
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if err := c.Store.LoadBootstrapTarget(ctx, &op); err != nil {
		return err
	}
	phase, err := c.currentPhase(ctx, op.ID)
	if err != nil {
		return err
	}
	if phase == api.ZoneValidating {
		var ok bool
		e := c.withLease(ctx, op, d, func(stepCtx context.Context) error {
			var validationErr error
			ok, validationErr = c.Executor.Validated(stepCtx, op)
			return validationErr
		})
		if e != nil {
			return e
		}
		if !ok {
			return fmt.Errorf("bootstrap operation %s has not passed validation", op.ID)
		}
		return c.Store.BootstrapPhase(ctx, op, api.ZoneReady, ok)
	}
	n, ok := bootstrapNext(op.Kind, phase)
	if !ok {
		return fmt.Errorf("bootstrap operation %s is in terminal phase %s", op.ID, phase)
	}
	var result api.BootstrapStepResult
	if err = c.withLease(ctx, op, d, func(stepCtx context.Context) error {
		var stepErr error
		result, stepErr = c.Executor.Step(stepCtx, op, n)
		return stepErr
	}); err != nil {
		return err
	}
	if n == api.ZoneControlPlaneInitializing {
		if err := c.Store.SetClusterDiscovery(ctx, op, result); err != nil {
			return err
		}
	} else if n == api.ZoneInfrastructureProvisioning {
		if err := c.Store.RecordBootstrapHost(ctx, op, result); err != nil {
			return err
		}
	} else if n == api.ZoneDominatorInstalling {
		if err := c.Store.SetDominatorRoute(ctx, op, result); err != nil {
			return err
		}
	} else if result.APIServerEndpoint != "" || result.CACertHash != "" {
		return fmt.Errorf("unexpected cluster discovery from %s", n)
	}
	if err := c.Store.BootstrapPhase(ctx, op, n, false); err != nil {
		return err
	}
	return c.Store.ReleaseBootstrapLease(ctx, op)
}

// withLease keeps long remote steps fenced. A lost lease cancels the step;
// provider executors must use the supplied context for every remote call.
func (c *BootstrapController) withLease(ctx context.Context, op api.Operation, lease time.Duration, run func(context.Context) error) error {
	stepCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	leaseErr := make(chan error, 1)
	interval := lease / 3
	if interval < time.Millisecond {
		interval = time.Millisecond
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				leaseErr <- nil
				return
			case <-stepCtx.Done():
				leaseErr <- stepCtx.Err()
				return
			case <-ticker.C:
				if err := c.Store.Renew(stepCtx, op, lease); err != nil {
					cancel()
					leaseErr <- err
					return
				}
			}
		}
	}()
	err := run(stepCtx)
	close(done)
	if e := <-leaseErr; e != nil {
		return e
	}
	return err
}

func (c *BootstrapController) currentPhase(ctx context.Context, operationID string) (api.ZoneBootstrapPhase, error) {
	var phase api.ZoneBootstrapPhase
	err := c.Store.DB.QueryRowContext(ctx, `SELECT phase FROM zone_bootstrap_operations WHERE operation_id=$1`, operationID).Scan(&phase)
	return phase, err
}
