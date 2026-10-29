package telemetry_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/nabutabu/crane-oss/internal/capacity"
	"github.com/nabutabu/crane-oss/internal/logging"
	"github.com/nabutabu/crane-oss/internal/telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestHTTPObservation(t *testing.T) {
	output := captureOutput(t)
	t.Setenv("LOG_LEVEL", "info")
	logger := logging.NewWithOTLP("crane-api", nil)
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer tp.Shutdown(context.Background())
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer mp.Shutdown(context.Background())
	p := &telemetry.Provider{Tracer: tp.Tracer("test"), Meter: mp.Meter("test")}
	previous := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	defer otel.SetTextMapPropagator(previous)
	inner := http.NewServeMux()
	inner.HandleFunc("GET /v1/nodes/{id}", func(w http.ResponseWriter, r *http.Request) {
		logger.InfoContext(r.Context(), "inside request")
		if _, ok := w.(http.Flusher); !ok {
			t.Error("lost flusher")
		}
		fmt.Fprint(w, "ok")
	})
	inner.HandleFunc("POST /v1/fail", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) })
	inner.HandleFunc("GET /v1/panic", func(w http.ResponseWriter, r *http.Request) { panic("private-panic-value") })
	mux := http.NewServeMux()
	mux.Handle("/v1/", inner)
	mux.Handle("/admin/", capacity.NewAPI(nil, "admin", map[string]string{"cluster": "adapter"}).Handler())
	observed := p.HTTPHandler(mux, logger.Logger)
	for _, tc := range []struct {
		method, path, route string
		status              int
		panics              bool
	}{
		{"GET", "/v1/nodes/private-path-value?token=query-value", "/v1/nodes/{id}", 200, false},
		{"POST", "/v1/fail", "/v1/fail", 503, false},
		{"GET", "/unknown-private-value", "unmatched", 404, false},
		{"GET", "/v1/panic", "/v1/panic", 500, true},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader("private-body-value"))
		req.Header.Set("Authorization", "Bearer private-header-value")
		req.Header.Set("X-Request-ID", "private-request-id-value")
		req.Header.Set("traceparent", "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01")
		response := httptest.NewRecorder()
		func() {
			defer func() {
				if (recover() != nil) != tc.panics {
					t.Errorf("unexpected panic behavior: %s", tc.path)
				}
			}()
			observed.ServeHTTP(response, req)
		}()
		if _, err := uuid.Parse(response.Header().Get("X-Request-ID")); err != nil {
			t.Fatal("request ID must be generated")
		}
		if !tc.panics && response.Code != tc.status {
			t.Errorf("response changed: %d", response.Code)
		}
		spans := exporter.GetSpans()
		span := spans[len(spans)-1]
		if span.Name != tc.method+" "+tc.route || span.Parent.TraceID().String() != "0123456789abcdef0123456789abcdef" {
			t.Fatalf("lost matched route or parent: %+v", span)
		}
		if (span.Status.Code == codes.Error) != (tc.status >= 500) {
			t.Fatal("wrong span status")
		}
	}
	// Authentication failures on the actual API routes are also observed.
	auth := http.NewServeMux()
	auth.Handle("/v1/", capacity.NewAPI(nil, "admin", map[string]string{"cluster": "adapter"}).Handler())
	r := httptest.NewRecorder()
	p.HTTPHandler(auth, logger.Logger).ServeHTTP(r, httptest.NewRequest("POST", "/v1/zones", nil))
	if r.Code != http.StatusUnauthorized {
		t.Fatal("authentication boundary changed")
	}
	data, _ := os.ReadFile(output.Name())
	spansData, _ := json.Marshal(exporter.GetSpans())
	for destination, content := range map[string]string{"logs": string(data), "traces": string(spansData)} {
		for _, secret := range []string{"private-path-value", "query-value", "private-body-value", "private-header-value", "private-request-id-value", "unknown-private-value", "private-panic-value"} {
			if strings.Contains(content, secret) {
				t.Fatalf("%s captured private request data: %s", destination, secret)
			}
		}
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var first, completed map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &completed); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"request_id", "trace_id", "span_id"} {
		if first[key] == nil || first[key] != completed[key] {
			t.Fatalf("missing request processing correlation: %s", key)
		}
	}
	if completed["route"] != "/v1/nodes/{id}" || completed["status"] != float64(200) || completed["duration_ms"] == nil {
		t.Fatalf("missing completion fields: %v", completed)
	}
	var metrics metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &metrics); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, scope := range metrics.ScopeMetrics {
		for _, m := range scope.Metrics {
			names[m.Name] = true
		}
	}
	if !names["http.server.request.duration"] || !names["http.server.request.count"] {
		t.Fatalf("missing request metrics: %v", names)
	}
}
