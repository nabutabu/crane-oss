package capacity

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

func TestAdapterPreservesServerSpan(t *testing.T) {
	previous := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	defer otel.SetTextMapPropagator(previous)
	id, _ := trace.TraceIDFromHex("11111111111111111111111111111111")
	spanID, _ := trace.SpanIDFromHex("2222222222222222")
	sc := trace.NewSpanContext(trace.SpanContextConfig{TraceID: id, SpanID: spanID})
	api := NewAPI(nil, "admin", "adapter", "cluster")
	for _, active := range []bool{false, true} {
		request := httptest.NewRequest("POST", "/v1/capacity", nil)
		request.Header.Set("Authorization", "Bearer adapter")
		request.Header.Set("X-Crane-Cluster-ID", "cluster")
		request.Header.Set("traceparent", "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01")
		if active {
			request = request.WithContext(trace.ContextWithSpanContext(context.Background(), sc))
		}
		called := false
		api.adapter(func(w http.ResponseWriter, r *http.Request) {
			called = true
			got := trace.SpanContextFromContext(r.Context())
			if active && (got.TraceID() != sc.TraceID() || got.SpanID() != sc.SpanID()) {
				t.Fatal("adapter replaced server span")
			}
			if !active && got.TraceID().String() != "0123456789abcdef0123456789abcdef" {
				t.Fatal("standalone adapter lost propagation")
			}
		}).ServeHTTP(httptest.NewRecorder(), request)
		if !called {
			t.Fatal("adapter handler not called")
		}
	}
}
