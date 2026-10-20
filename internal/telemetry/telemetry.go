package telemetry

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/log"
	lognoop "go.opentelemetry.io/otel/log/noop"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.34.0"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

const EnvOTLPEndpoint = "OTEL_EXPORTER_OTLP_ENDPOINT"

type Config struct {
	ServiceName    string
	ServiceVersion string
	Environment    string
	// EnableLogs is deliberately opt-in at each service entrypoint.
	EnableLogs bool
	// ErrorHandler must write locally, without using an exporting handler.
	ErrorHandler otel.ErrorHandler
}

type ShutdownFunc func(context.Context) error

type Provider struct {
	TracerProvider trace.TracerProvider
	MeterProvider  metric.MeterProvider
	LoggerProvider log.LoggerProvider
	Tracer         trace.Tracer
	Meter          metric.Meter
	Shutdown       ShutdownFunc
}

// New enables each signal when a common or signal-specific endpoint is set.
// Exporters read standard OTLP/gRPC endpoint, header, TLS and timeout variables.
// Initialization failure returns an error and usable no-op providers. Export is
// asynchronous: collector outages never disable local application logging.
func New(ctx context.Context, cfg Config) (*Provider, error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	if cfg.ErrorHandler != nil {
		otel.SetErrorHandler(cfg.ErrorHandler)
	}
	signals := []string{"TRACES", "METRICS"}
	if cfg.EnableLogs {
		signals = append(signals, "LOGS")
	}
	enabled := false
	for _, signal := range signals {
		if err := validateSignal(signal); err != nil {
			return newNoopProvider(cfg), err
		}
		enabled = enabled || signalEnabled(signal)
	}
	if !enabled {
		return newNoopProvider(cfg), nil
	}
	p, err := newOTLPProvider(ctx, cfg)
	if err != nil {
		// Exporter errors can include endpoint/header configuration. Do not return
		// their raw text, even to a redacting logger.
		return newNoopProvider(cfg), errors.New("OTLP initialization failed; check endpoint, TLS and exporter configuration")
	}
	return p, nil
}

func signalEnabled(signal string) bool {
	return strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_"+signal+"_ENDPOINT")) != "" || strings.TrimSpace(os.Getenv(EnvOTLPEndpoint)) != ""
}

func validateSignal(signal string) error {
	protocol := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_" + signal + "_PROTOCOL"))
	if protocol == "" {
		protocol = strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL"))
	}
	if protocol != "" && protocol != "grpc" {
		return fmt.Errorf("unsupported OTLP %s protocol; use grpc", strings.ToLower(signal))
	}
	endpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_" + signal + "_ENDPOINT"))
	if endpoint == "" {
		endpoint = strings.TrimSpace(os.Getenv(EnvOTLPEndpoint))
	}
	if strings.Contains(endpoint, "://") {
		u, err := url.Parse(endpoint)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return fmt.Errorf("invalid OTLP %s endpoint URL", strings.ToLower(signal))
		}
	}
	return nil
}

func newNoopProvider(cfg Config) *Provider {
	tp := tracenoop.NewTracerProvider()
	mp := metricnoop.NewMeterProvider()
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	return &Provider{TracerProvider: tp, MeterProvider: mp, LoggerProvider: lognoop.NewLoggerProvider(), Tracer: tp.Tracer(cfg.ServiceName), Meter: mp.Meter(cfg.ServiceName), Shutdown: func(context.Context) error { return nil }}
}

func newOTLPProvider(ctx context.Context, cfg Config) (_ *Provider, resultErr error) {
	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceNameKey.String(cfg.ServiceName), semconv.ServiceVersionKey.String(cfg.ServiceVersion), semconv.DeploymentEnvironmentName(cfg.Environment)),
		resource.WithFromEnv(),
	)
	if err != nil {
		return nil, err
	}
	p := newNoopProvider(cfg)
	var shutdowns []ShutdownFunc
	shutdown := func(ctx context.Context) error {
		// Give every signal the same bounded deadline concurrently so one blocked
		// exporter cannot consume the entire shutdown budget of the others.
		errs := make([]error, len(shutdowns))
		var wg sync.WaitGroup
		for i, fn := range shutdowns {
			wg.Add(1)
			go func() { defer wg.Done(); errs[i] = fn(ctx) }()
		}
		wg.Wait()
		return errors.Join(errs...)
	}
	defer func() {
		if resultErr != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = shutdown(cleanup)
		}
	}()
	if signalEnabled("TRACES") {
		exporter, err := otlptracegrpc.New(ctx)
		if err != nil {
			return nil, err
		}
		tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter), sdktrace.WithResource(res))
		p.TracerProvider, p.Tracer = tp, tp.Tracer(cfg.ServiceName)
		shutdowns = append(shutdowns, tp.Shutdown)
	}
	if signalEnabled("METRICS") {
		exporter, err := otlpmetricgrpc.New(ctx)
		if err != nil {
			return nil, err
		}
		mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter, sdkmetric.WithInterval(10*time.Second))), sdkmetric.WithResource(res))
		p.MeterProvider, p.Meter = mp, mp.Meter(cfg.ServiceName)
		shutdowns = append(shutdowns, mp.Shutdown)
	}
	if cfg.EnableLogs && signalEnabled("LOGS") {
		exporter, err := otlploggrpc.New(ctx)
		if err != nil {
			return nil, err
		}
		lp := sdklog.NewLoggerProvider(sdklog.WithResource(res), sdklog.WithProcessor(sdklog.NewBatchProcessor(exporter,
			sdklog.WithMaxQueueSize(2048), sdklog.WithExportMaxBatchSize(512), sdklog.WithExportInterval(time.Second),
		)))
		p.LoggerProvider = lp
		shutdowns = append(shutdowns, lp.Shutdown)
	}
	otel.SetTracerProvider(p.TracerProvider)
	otel.SetMeterProvider(p.MeterProvider)
	p.Shutdown = shutdown
	return p, nil
}
