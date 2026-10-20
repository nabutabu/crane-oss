package main

import (
	"context"
	"log"
	"log/slog"
	"net"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	collector "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	"google.golang.org/grpc"
)

type startupReceiver struct {
	collector.UnimplementedLogsServiceServer
	mu       sync.Mutex
	messages []string
}

func (s *startupReceiver) Export(_ context.Context, r *collector.ExportLogsServiceRequest) (*collector.ExportLogsServiceResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, resource := range r.ResourceLogs {
		for _, scope := range resource.ScopeLogs {
			for _, record := range scope.LogRecords {
				s.messages = append(s.messages, record.Body.GetStringValue())
			}
		}
	}
	return &collector.ExportLogsServiceResponse{}, nil
}

func TestStartupFailureIsExportedBeforeShutdown(t *testing.T) {
	oldLogger, oldWriter, oldFlags, oldPrefix := slog.Default(), log.Writer(), log.Flags(), log.Prefix()
	oldTP, oldMP, oldErrors, oldProp := otel.GetTracerProvider(), otel.GetMeterProvider(), otel.GetErrorHandler(), otel.GetTextMapPropagator()
	defer func() {
		slog.SetDefault(oldLogger)
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
		log.SetPrefix(oldPrefix)
		otel.SetTracerProvider(oldTP)
		otel.SetMeterProvider(oldMP)
		otel.SetErrorHandler(oldErrors)
		otel.SetTextMapPropagator(oldProp)
	}()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	receiver := &startupReceiver{}
	server := grpc.NewServer()
	collector.RegisterLogsServiceServer(server, receiver)
	go server.Serve(listener)
	defer server.Stop()
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", "http://"+listener.Addr().String())
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "grpc")
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_PROTOCOL", "grpc")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "")
	t.Setenv("LOG_LEVEL", "info")
	t.Setenv("CRANE_DATABASE_URL", "invalid-database-config")
	if err := run(); err == nil {
		t.Fatal("expected database configuration failure")
	}
	receiver.mu.Lock()
	defer receiver.mu.Unlock()
	if len(receiver.messages) != 2 || receiver.messages[0] != "crane-api starting" || receiver.messages[1] != "crane-api stopped" {
		t.Fatalf("startup and terminal error must flush before returning: %v", receiver.messages)
	}
}
