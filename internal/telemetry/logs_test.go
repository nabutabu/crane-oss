package telemetry_test

import (
	"context"
	"encoding/json"
	"log"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nabutabu/crane-oss/internal/logging"
	"github.com/nabutabu/crane-oss/internal/telemetry"
	"go.opentelemetry.io/otel"
	lognoop "go.opentelemetry.io/otel/log/noop"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	collector "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type logReceiver struct {
	collector.UnimplementedLogsServiceServer
	mu       sync.Mutex
	requests []*collector.ExportLogsServiceRequest
	fail     bool
	block    bool
}

func (s *logReceiver) Export(ctx context.Context, r *collector.ExportLogsServiceRequest) (*collector.ExportLogsServiceResponse, error) {
	if s.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, r)
	if s.fail {
		return nil, status.Error(codes.InvalidArgument, "synthetic backend failure")
	}
	return &collector.ExportLogsServiceResponse{}, nil
}

func configure(t *testing.T, receiver *logReceiver) {
	t.Helper()
	for _, signal := range []string{"", "LOGS_", "TRACES_", "METRICS_"} {
		for _, setting := range []string{"ENDPOINT", "PROTOCOL", "HEADERS", "INSECURE", "CERTIFICATE", "CLIENT_CERTIFICATE", "CLIENT_KEY", "TIMEOUT"} {
			t.Setenv("OTEL_EXPORTER_OTLP_"+signal+setting, "")
		}
	}
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "deployment.environment.name=test,k8s.cluster.name=layer-zero,service.instance.id=api-1")
	t.Setenv("OTEL_SERVICE_NAME", "api-acceptance")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	collector.RegisterLogsServiceServer(server, receiver)
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", "http://"+listener.Addr().String())
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_PROTOCOL", "grpc")
	oldTP, oldMP := otel.GetTracerProvider(), otel.GetMeterProvider()
	t.Cleanup(func() { otel.SetTracerProvider(oldTP); otel.SetMeterProvider(oldMP) })
}

func captureOutput(t *testing.T) *os.File {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	original, defaultLogger := os.Stdout, slog.Default()
	writer, flags, prefix := log.Writer(), log.Flags(), log.Prefix()
	os.Stdout = f
	t.Cleanup(func() {
		os.Stdout = original
		slog.SetDefault(defaultLogger)
		log.SetOutput(writer)
		log.SetFlags(flags)
		log.SetPrefix(prefix)
		f.Close()
	})
	return f
}

func TestOTLPLogsRedactionCorrelationAndShutdown(t *testing.T) {
	for _, level := range []string{"info", "debug"} {
		t.Run(level, func(t *testing.T) {
			receiver := &logReceiver{}
			configure(t, receiver)
			t.Setenv("LOG_LEVEL", level)
			t.Setenv("LOG_FORMAT", "text") // API logging must still use JSON.
			output := captureOutput(t)
			p, err := telemetry.New(context.Background(), telemetry.Config{ServiceName: "crane-api", ServiceVersion: "test-version", EnableLogs: true})
			if err != nil {
				t.Fatal(err)
			}
			logger := logging.NewWithOTLP("crane-api", p.LoggerProvider)
			logger.SetDefault()
			tp := sdktrace.NewTracerProvider()
			defer tp.Shutdown(context.Background())
			ctx, span := tp.Tracer("test").Start(context.Background(), "operation")
			sc := trace.SpanContextFromContext(ctx)
			timestamp := time.Now().Add(-time.Second)
			record := slog.NewRecord(timestamp, slog.LevelWarn, "controlled error password=message-value", 0)
			record.AddAttrs(slog.Group("details", slog.String("password", "nested-value"), slog.Int("count", 7)))
			attached := logger.With("api_token", "attached-value", "payload", map[string]any{"secret": "map-value", "safe": "visible"}).WithGroup("work")
			if err := attached.Handler().Handle(ctx, record); err != nil {
				t.Fatal(err)
			}
			logger.DebugContext(ctx, "debug record")
			logger.InfoContext(ctx, "info record")
			log.Print("legacy password=legacy-value")
			span.End()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := p.Shutdown(shutdownCtx); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(output.Name())
			if err != nil {
				t.Fatal(err)
			}
			receiver.mu.Lock()
			defer receiver.mu.Unlock()
			exported, err := json.Marshal(receiver.requests)
			if err != nil {
				t.Fatal(err)
			}
			for destination, content := range map[string]string{"stdout": string(data), "otlp": string(exported)} {
				for _, secret := range []string{"message-value", "nested-value", "attached-value", "map-value", "legacy-value"} {
					if strings.Contains(content, secret) {
						t.Fatalf("%s leaked %s", destination, secret)
					}
				}
				if !strings.Contains(content, "[REDACTED]") || !strings.Contains(content, "visible") {
					t.Fatalf("%s lost redaction or safe fields: %s", destination, content)
				}
				if strings.Contains(content, "debug record") != (level == "debug") {
					t.Fatalf("%s ignored LOG_LEVEL", destination)
				}
			}
			if !json.Valid([]byte(strings.Split(string(data), "\n")[0])) {
				t.Fatalf("stdout must be JSON: %s", data)
			}
			var records []*logspb.LogRecord
			for _, request := range receiver.requests {
				for _, resource := range request.ResourceLogs {
					attrs, _ := json.Marshal(resource.Resource.Attributes)
					for _, value := range []string{"api-acceptance", "test-version", "layer-zero", "api-1"} {
						if !strings.Contains(string(attrs), value) {
							t.Fatalf("missing resource %s: %s", value, attrs)
						}
					}
					for _, scope := range resource.ScopeLogs {
						records = append(records, scope.LogRecords...)
					}
				}
			}
			expected := 3
			if level == "debug" {
				expected++
			}
			if len(records) != expected {
				t.Fatalf("got %d records, want %d", len(records), expected)
			}
			first := records[0]
			if first.TimeUnixNano != uint64(timestamp.UnixNano()) || first.SeverityNumber != logspb.SeverityNumber_SEVERITY_NUMBER_WARN {
				t.Fatalf("lost timestamp/severity: %v", first)
			}
			tid, sid := sc.TraceID(), sc.SpanID()
			if string(first.TraceId) != string(tid[:]) || string(first.SpanId) != string(sid[:]) {
				t.Fatal("missing native trace correlation")
			}
			if !strings.Contains(string(data), sc.TraceID().String()) {
				t.Fatal("stdout missing trace correlation")
			}
			attrs, _ := json.Marshal(first.Attributes)
			for _, field := range []string{"work", "details", "count", "IntValue"} {
				if !strings.Contains(string(attrs), field) {
					t.Fatalf("lost group/attribute %s: %s", field, attrs)
				}
			}
		})
	}
}

func TestLogExportIsOptIn(t *testing.T) {
	configure(t, &logReceiver{})
	p, err := telemetry.New(context.Background(), telemetry.Config{ServiceName: "other-binary"})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Shutdown(context.Background())
	if _, ok := p.LoggerProvider.(lognoop.LoggerProvider); !ok {
		t.Fatalf("services without EnableLogs must not export logs: %T", p.LoggerProvider)
	}
}

func TestLogExporterFailureKeepsStdoutAndDiagnosticsLocal(t *testing.T) {
	receiver := &logReceiver{fail: true}
	configure(t, receiver)
	output := captureOutput(t)
	local := logging.NewWithOTLP("diagnostics", nil)
	previous := otel.GetErrorHandler()
	t.Cleanup(func() { otel.SetErrorHandler(previous) })
	p, err := telemetry.New(context.Background(), telemetry.Config{ServiceName: "crane-api", EnableLogs: true, ErrorHandler: otel.ErrorHandlerFunc(func(error) {
		local.Warn("local export diagnostic")
	})})
	if err != nil {
		t.Fatal(err)
	}
	logger := logging.NewWithOTLP("crane-api", p.LoggerProvider)
	logger.SetDefault()
	logger.Info("application continues")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = p.Shutdown(ctx) // SDK reports exporter failure via ErrorHandler.
	data, _ := os.ReadFile(output.Name())
	if !strings.Contains(string(data), "application continues") {
		t.Fatal("stdout stopped working")
	}
	receiver.mu.Lock()
	defer receiver.mu.Unlock()
	exported, _ := json.Marshal(receiver.requests)
	if strings.Contains(string(exported), "local export diagnostic") {
		t.Fatal("recursive diagnostic export")
	}
	if len(receiver.requests) != 1 {
		t.Fatalf("unexpected recursive exports: %d", len(receiver.requests))
	}
}

func TestLogShutdownHonorsDeadline(t *testing.T) {
	configure(t, &logReceiver{block: true})
	captureOutput(t)
	p, err := telemetry.New(context.Background(), telemetry.Config{ServiceName: "crane-api", EnableLogs: true})
	if err != nil {
		t.Fatal(err)
	}
	logging.NewWithOTLP("crane-api", p.LoggerProvider).Info("queued before shutdown")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := p.Shutdown(ctx); err == nil {
		t.Fatal("expected deadline error")
	}
	if time.Since(start) > time.Second {
		t.Fatal("shutdown exceeded deadline")
	}
}
