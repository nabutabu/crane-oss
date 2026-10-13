package telemetry

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.34.0"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

// EnvOTLPEndpoint configures the OTLP gRPC collector endpoint. When unset,
// telemetry falls back to no-op providers.
const EnvOTLPEndpoint = "OTEL_EXPORTER_OTLP_ENDPOINT"

// Config holds telemetry configuration.
type Config struct {
	ServiceName    string
	ServiceVersion string
	Environment    string
}

// ShutdownFunc is a cleanup function.
type ShutdownFunc func(context.Context) error

// Provider holds telemetry providers.
type Provider struct {
	TracerProvider trace.TracerProvider
	MeterProvider  metric.MeterProvider
	Tracer         trace.Tracer
	Meter          metric.Meter
	Shutdown       ShutdownFunc
}

// New initializes OpenTelemetry with OTLP exporters (gRPC). When
// OTEL_EXPORTER_OTLP_ENDPOINT is unset, or when initialization fails, New
// returns a true no-op provider. In that failure case the returned error
// describes the problem and the provider is still safe to use and shut down.
func New(ctx context.Context, cfg Config) (*Provider, error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	endpoint := strings.TrimSpace(os.Getenv(EnvOTLPEndpoint))
	if endpoint == "" {
		return newNoopProvider(cfg), nil
	}
	if strings.Contains(endpoint, "://") {
		u, err := url.Parse(endpoint)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
			return newNoopProvider(cfg), fmt.Errorf("invalid OTLP endpoint URL")
		}
	}

	p, err := newOTLPProvider(ctx, cfg, endpoint)
	if err != nil {
		return newNoopProvider(cfg), fmt.Errorf("otlp telemetry init failed; continuing with no-op providers: %w", err)
	}
	return p, nil
}

func newNoopProvider(cfg Config) *Provider {
	tracerProvider := tracenoop.NewTracerProvider()
	meterProvider := metricnoop.NewMeterProvider()

	otel.SetTracerProvider(tracerProvider)
	otel.SetMeterProvider(meterProvider)

	return &Provider{
		TracerProvider: tracerProvider,
		MeterProvider:  meterProvider,
		Tracer:         tracerProvider.Tracer(cfg.ServiceName),
		Meter:          meterProvider.Meter(cfg.ServiceName),
		Shutdown:       func(context.Context) error { return nil },
	}
}

func newOTLPProvider(ctx context.Context, cfg Config, endpoint string) (*Provider, error) {
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceNameKey.String(cfg.ServiceName),
			semconv.ServiceVersionKey.String(cfg.ServiceVersion),
			semconv.DeploymentEnvironmentName(cfg.Environment),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource: %w", err)
	}

	tracerProvider, err := initTracerProvider(ctx, res, endpoint)
	if err != nil {
		return nil, err
	}

	meterProvider, err := initMeterProvider(ctx, res, endpoint)
	if err != nil {
		_ = tracerProvider.Shutdown(ctx)
		return nil, err
	}

	otel.SetTracerProvider(tracerProvider)
	otel.SetMeterProvider(meterProvider)

	shutdown := func(ctx context.Context) error {
		var errs []error
		if err := tracerProvider.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
		if err := meterProvider.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
		if len(errs) > 0 {
			return fmt.Errorf("shutdown errors: %v", errs)
		}
		return nil
	}

	return &Provider{
		TracerProvider: tracerProvider,
		MeterProvider:  meterProvider,
		Tracer:         tracerProvider.Tracer(cfg.ServiceName),
		Meter:          meterProvider.Meter(cfg.ServiceName),
		Shutdown:       shutdown,
	}, nil
}

func initTracerProvider(ctx context.Context, res *resource.Resource, endpoint string) (*sdktrace.TracerProvider, error) {
	options := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(endpoint), otlptracegrpc.WithInsecure()}
	if strings.Contains(endpoint, "://") {
		options = []otlptracegrpc.Option{otlptracegrpc.WithEndpointURL(endpoint)}
	}
	exporter, err := otlptracegrpc.New(ctx, options...)
	if err != nil {
		return nil, fmt.Errorf("failed to create OTLP trace exporter: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter, sdktrace.WithBatchTimeout(5*time.Second)),
		sdktrace.WithResource(res),
	)
	return tp, nil
}

func initMeterProvider(ctx context.Context, res *resource.Resource, endpoint string) (*sdkmetric.MeterProvider, error) {
	options := []otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpoint(endpoint), otlpmetricgrpc.WithInsecure()}
	if strings.Contains(endpoint, "://") {
		options = []otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpointURL(endpoint)}
	}
	exporter, err := otlpmetricgrpc.New(ctx, options...)
	if err != nil {
		return nil, fmt.Errorf("failed to create OTLP metric exporter: %w", err)
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter, sdkmetric.WithInterval(10*time.Second))),
		sdkmetric.WithResource(res),
	)
	return mp, nil
}
