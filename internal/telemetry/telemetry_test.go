package telemetry

import (
	"context"
	"testing"
)

func TestNew_NoEndpoint(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	p, err := New(context.Background(), Config{
		ServiceName:    "test-service",
		ServiceVersion: "0.1.0",
		Environment:    "test",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if p == nil {
		t.Fatal("expected provider")
	}
	if p.Tracer == nil {
		t.Fatal("expected tracer")
	}
	if p.Meter == nil {
		t.Fatal("expected meter")
	}
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown err: %v", err)
	}
}
