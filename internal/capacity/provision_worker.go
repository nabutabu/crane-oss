package capacity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/nabutabu/crane-oss/pkg/api"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

type ProvisionResult struct {
	ProviderID string
	PrivateIP  string
}

// ProvisionExecutor contains provider-specific work while the capacity store
// retains operation ownership and lifecycle authority.
type ProvisionExecutor interface {
	Launch(context.Context, api.Operation) (ProvisionResult, error)
	EnrollAndJoin(context.Context, api.Operation, api.Host) error
	Ready(context.Context, api.Operation, api.Host) (bool, error)
}

type ProvisionController struct {
	Store    *Store
	Executor ProvisionExecutor
	ZoneID   string
	Identity string
	Lease    time.Duration
}

func (c *ProvisionController) RunOnce(ctx context.Context) error {
	if c.Store == nil || c.Executor == nil || c.ZoneID == "" || c.Identity == "" {
		return errors.New("provisioning worker is not configured")
	}
	d := c.Lease
	if d == 0 {
		d = 2 * time.Minute
	}
	op, err := c.Store.ClaimDominator(ctx, c.ZoneID, "provision", c.Identity, d)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	ctx, span := otel.Tracer("crane/capacity").Start(ctx, "capacity.provision")
	started := time.Now()
	attrs := []attribute.KeyValue{attribute.String("zone_id", op.ZoneID), attribute.String("operation_id", op.ID), attribute.String("host_id", op.HostID)}
	span.SetAttributes(attrs...)
	span.AddEvent("provisioning operation claimed")
	defer span.End()
	if err = c.Store.LoadProvisioningTarget(ctx, &op); err != nil {
		span.SetStatus(codes.Error, "trusted provisioning target unavailable")
		recordProvisionMetrics(ctx, started, "target_unavailable")
		return err
	}
	err = c.withLease(ctx, op, d, func(stepCtx context.Context) error {
		return c.advance(stepCtx, op)
	})
	outcome := "progressed"
	if err != nil {
		outcome = "retryable_failure"
		span.SetStatus(codes.Error, "provisioning step failed")
		span.RecordError(errors.New("provisioning step failed"))
		slog.WarnContext(ctx, "capacity provisioning step failed", "zone_id", op.ZoneID, "operation_id", op.ID, "host_id", op.HostID)
	} else {
		span.AddEvent("provisioning step completed")
		slog.InfoContext(ctx, "capacity provisioning progress recorded", "zone_id", op.ZoneID, "operation_id", op.ID, "host_id", op.HostID)
	}
	recordProvisionMetrics(ctx, started, outcome)
	return err
}

func recordProvisionMetrics(ctx context.Context, started time.Time, outcome string) {
	attrs := metric.WithAttributes(attribute.String("outcome", outcome))
	count, _ := otel.Meter("crane/capacity").Int64Counter("crane.capacity.provision.operation.count")
	duration, _ := otel.Meter("crane/capacity").Float64Histogram("crane.capacity.provision.operation.duration", metric.WithUnit("s"))
	count.Add(ctx, 1, attrs)
	duration.Record(ctx, time.Since(started).Seconds(), attrs)
}

func (c *ProvisionController) advance(ctx context.Context, op api.Operation) error {
	host, err := c.Store.Host(ctx, op.HostID)
	if err != nil {
		return err
	}
	switch host.State {
	case api.HostRequested, api.HostProvisioning:
		provisionStage(ctx, "launch")
		if err := c.Store.BeginLaunch(ctx, op); err != nil {
			return err
		}
		if host.State == api.HostRequested {
			if err := c.Store.SetProvisionState(ctx, op, api.HostRequested, api.HostProvisioning); err != nil {
				return err
			}
		}
		result, err := c.Executor.Launch(ctx, op)
		if err != nil {
			return c.retryable(ctx, op, "provider_launch", err)
		}
		if err := c.Store.RecordLaunchWithAddress(ctx, op, result.ProviderID, result.PrivateIP); err != nil {
			return err
		}
		return c.Store.ReleaseProvisionLease(ctx, op)
	case api.HostBooting:
		provisionStage(ctx, "enrollment")
		if err := c.Store.SetProvisionState(ctx, op, api.HostBooting, api.HostRegistering); err != nil {
			return err
		}
		fallthrough
	case api.HostRegistering:
		provisionStage(ctx, "enrollment")
		if err := c.Executor.EnrollAndJoin(ctx, op, host); err != nil {
			return c.retryable(ctx, op, "host_enrollment", err)
		}
		if err := c.Store.SetProvisionState(ctx, op, api.HostRegistering, api.HostConverging); err != nil {
			return err
		}
		return c.Store.ReleaseProvisionLease(ctx, op)
	case api.HostConverging:
		provisionStage(ctx, "readiness")
		ready, err := c.Executor.Ready(ctx, op, host)
		if err != nil {
			return c.retryable(ctx, op, "readiness_check", err)
		}
		if !ready {
			waiting, _ := otel.Meter("crane/capacity").Int64Counter("crane.capacity.provision.readiness.wait.count")
			waiting.Add(ctx, 1)
			trace.SpanFromContext(ctx).AddEvent("waiting for host readiness gates")
			return c.Store.ReleaseProvisionLease(ctx, op)
		}
		if err := c.Store.SetProvisionState(ctx, op, api.HostConverging, api.HostReady); err != nil {
			return err
		}
		return c.Store.CompleteProvision(ctx, op)
	case api.HostReady:
		return c.Store.CompleteProvision(ctx, op)
	default:
		return fmt.Errorf("provision operation %s has host in non-runnable state %s", op.ID, host.State)
	}
}

func provisionStage(ctx context.Context, stage string) {
	trace.SpanFromContext(ctx).AddEvent("provisioning stage", trace.WithAttributes(attribute.String("stage", stage)))
}

func (c *ProvisionController) retryable(ctx context.Context, op api.Operation, category string, cause error) error {
	span := trace.SpanFromContext(ctx)
	span.SetAttributes(attribute.String("failure_category", category))
	slog.WarnContext(ctx, "capacity provisioning stage will retry", "zone_id", op.ZoneID, "operation_id", op.ID, "host_id", op.HostID, "failure_category", category)
	if err := c.Store.RecordProvisionFailure(ctx, op, category); err != nil {
		return err
	}
	if err := c.Store.ReleaseProvisionLease(ctx, op); err != nil {
		return err
	}
	return fmt.Errorf("%s: %w", category, cause)
}

func (c *ProvisionController) Run(ctx context.Context, interval time.Duration, logger *slog.Logger) error {
	if interval <= 0 {
		return errors.New("provisioning poll interval must be positive")
	}
	if logger == nil {
		logger = slog.Default()
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := c.RunOnce(ctx); err != nil && ctx.Err() == nil {
			logger.WarnContext(ctx, "capacity provisioning step failed", "zone_id", c.ZoneID)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (c *ProvisionController) withLease(ctx context.Context, op api.Operation, lease time.Duration, run func(context.Context) error) error {
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
	if leaseErr := <-leaseErr; leaseErr != nil {
		return leaseErr
	}
	return err
}
