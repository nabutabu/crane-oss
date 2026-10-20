package telemetry

import (
	"context"
	"testing"

	metricnoop "go.opentelemetry.io/otel/metric/noop"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

func TestNew_NoEndpoint(t *testing.T) {
	t.Setenv(EnvOTLPEndpoint, "")
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

func TestNew_NoEndpoint_UsesNoopProviders(t *testing.T) {
	t.Setenv(EnvOTLPEndpoint, "")
	p, err := New(context.Background(), Config{ServiceName: "test-service"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if _, ok := p.TracerProvider.(tracenoop.TracerProvider); !ok {
		t.Fatalf("expected no-op tracer provider, got %T", p.TracerProvider)
	}
	if _, ok := p.MeterProvider.(metricnoop.MeterProvider); !ok {
		t.Fatalf("expected no-op meter provider, got %T", p.MeterProvider)
	}
}

func TestNew_InvalidEndpointFallsBack(t *testing.T) {
	t.Setenv(EnvOTLPEndpoint, "https://")
	p, err := New(context.Background(), Config{ServiceName: "test-service"})
	if err == nil || p == nil {
		t.Fatal("expected error and usable fallback provider")
	}
	if _, ok := p.TracerProvider.(tracenoop.TracerProvider); !ok {
		t.Fatalf("expected no-op tracer, got %T", p.TracerProvider)
	}
	if _, ok := p.MeterProvider.(metricnoop.MeterProvider); !ok {
		t.Fatalf("expected no-op meter, got %T", p.MeterProvider)
	}
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestUnsupportedProtocolAndSignalEndpoint(t *testing.T) {
	t.Setenv(EnvOTLPEndpoint, "")
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", "https://collector.example:4317")
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_PROTOCOL", "http/protobuf")
	p, err := New(context.Background(), Config{ServiceName: "test", EnableLogs: true})
	if err == nil || p == nil {
		t.Fatal("expected safe fallback and protocol rejection")
	}
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_PROTOCOL", "grpc")
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", "https://user:password@collector.example:4317")
	p, err = New(context.Background(), Config{ServiceName: "test", EnableLogs: true})
	if err == nil || p == nil {
		t.Fatal("expected safe endpoint rejection")
	}
}
