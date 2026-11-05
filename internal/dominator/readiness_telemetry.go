package dominator

import (
	"context"
	"github.com/nabutabu/crane-oss/pkg/api"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"log/slog"
)

func recordWorkerGate(ctx context.Context, op api.Operation, gate string, passed bool) {
	outcome := "waiting"
	if passed {
		outcome = "passed"
	}
	attrs := []attribute.KeyValue{attribute.String("gate", gate), attribute.String("outcome", outcome)}
	count, _ := otel.Meter("crane/dominator").Int64Counter("crane.capacity.worker.readiness.gate.count")
	count.Add(ctx, 1, metric.WithAttributes(attrs...))
	trace.SpanFromContext(ctx).AddEvent("worker readiness gate", trace.WithAttributes(attrs...))
	slog.DebugContext(ctx, "worker readiness gate", "operation_id", op.ID, "host_id", op.HostID, "cluster_id", op.ClusterID, "zone_id", op.ZoneID, "gate", gate, "outcome", outcome)
}
