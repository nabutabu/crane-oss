package logging

import (
	"context"
	"errors"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// fanoutHandler receives only redacted records. The local handler is always
// called, even if remote handling fails. Never log exporter failures here.
type fanoutHandler struct {
	local, remote slog.Handler
	level         slog.Level
}

func (h *fanoutHandler) Enabled(_ context.Context, level slog.Level) bool { return level >= h.level }
func (h *fanoutHandler) Handle(ctx context.Context, r slog.Record) error {
	localErr := h.local.Handle(ctx, r.Clone())
	remoteErr := h.remote.Handle(ctx, r.Clone())
	return errors.Join(localErr, remoteErr)
}
func (h *fanoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &fanoutHandler{local: h.local.WithAttrs(attrs), remote: h.remote.WithAttrs(attrs), level: h.level}
}
func (h *fanoutHandler) WithGroup(name string) slog.Handler {
	return &fanoutHandler{local: h.local.WithGroup(name), remote: h.remote.WithGroup(name), level: h.level}
}

type requestIDKey struct{}

// WithRequestID attaches a server-generated request ID to context-aware logs.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// correlationHandler adds IDs from the actual context, never from caller input.
// The OTel bridge also sets the record's native trace/span ID fields.
type correlationHandler struct{ inner slog.Handler }

func (h *correlationHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}
func (h *correlationHandler) Handle(ctx context.Context, r slog.Record) error {
	if id, ok := ctx.Value(requestIDKey{}).(string); ok {
		r = r.Clone()
		r.AddAttrs(slog.String("request_id", id))
	}
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r = r.Clone()
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	return h.inner.Handle(ctx, r)
}
func (h *correlationHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &correlationHandler{inner: h.inner.WithAttrs(attrs)}
}
func (h *correlationHandler) WithGroup(name string) slog.Handler {
	return &correlationHandler{inner: h.inner.WithGroup(name)}
}
