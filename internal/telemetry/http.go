package telemetry

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/felixge/httpsnoop"
	"github.com/google/uuid"
	"github.com/nabutabu/crane-oss/internal/logging"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// HTTPHandler observes buffered and streaming routes without recording request
// headers, bodies, raw paths or queries. Route patterns come only from ServeMux.
func (p *Provider) HTTPHandler(next http.Handler, logger *slog.Logger) http.Handler {
	duration, _ := p.Meter.Float64Histogram("http.server.request.duration", metric.WithUnit("s"))
	requests, _ := p.Meter.Int64Counter("http.server.request.count", metric.WithUnit("{request}"))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := requestMethod(r.Method)
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := p.Tracer.Start(ctx, "HTTP request", trace.WithSpanKind(trace.SpanKindServer))
		id := uuid.NewString()
		ctx = logging.WithRequestID(ctx, id)
		r = r.WithContext(ctx)
		w.Header().Set("X-Request-ID", id)
		var panicValue any
		result := httpsnoop.CaptureMetricsFn(w, func(w http.ResponseWriter) {
			defer func() { panicValue = recover() }()
			next.ServeHTTP(w, r)
		})
		route := r.Pattern
		if _, path, found := strings.Cut(route, " "); found {
			route = path
		}
		if route == "" {
			route = "unmatched"
		}
		status := result.Code
		if panicValue != nil {
			status = http.StatusInternalServerError
		}
		attrs := []attribute.KeyValue{
			attribute.String("http.request.method", method), attribute.String("http.route", route), attribute.Int("http.response.status_code", status),
		}
		span.SetName(method + " " + route)
		span.SetAttributes(attrs...)
		span.SetAttributes(attribute.String("request_id", id))
		if status >= 500 {
			span.SetStatus(codes.Error, "HTTP server error")
		}
		duration.Record(ctx, result.Duration.Seconds(), metric.WithAttributes(attrs...))
		requests.Add(ctx, 1, metric.WithAttributes(attrs...))
		level := slog.LevelInfo
		if status >= 500 {
			level = slog.LevelError
		} else if status >= 400 {
			level = slog.LevelWarn
		}
		logger.Log(ctx, level, "HTTP request completed", "method", method, "route", route, "status", status, "duration_ms", float64(result.Duration)/float64(time.Millisecond), "panicked", panicValue != nil)
		// End before rethrowing: a directly deferred Span.End would record the raw
		// panic value as an exception event. Panic values can contain secrets.
		span.End()
		if panicValue != nil {
			panic(panicValue)
		}
	})
}

func requestMethod(method string) string {
	switch method {
	case "GET", "HEAD", "POST", "PUT", "DELETE", "CONNECT", "OPTIONS", "TRACE", "PATCH":
		return method
	default:
		return "_OTHER"
	}
}
