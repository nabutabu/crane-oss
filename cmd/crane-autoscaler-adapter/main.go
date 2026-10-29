package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nabutabu/crane-oss/internal/adapter/autoscaler"
	pb "github.com/nabutabu/crane-oss/internal/adapter/autoscaler/protos"
	"github.com/nabutabu/crane-oss/internal/logging"
	"github.com/nabutabu/crane-oss/internal/telemetry"
	"github.com/nabutabu/crane-oss/internal/version"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func main() {
	logger := logging.New("crane-autoscaler-adapter")
	logger.SetDefault()
	if err := run(); err != nil {
		slog.Error("adapter stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	addr := flag.String("listen", "127.0.0.1:50051", "gRPC listen address")
	craneURL := flag.String("crane-url", "http://127.0.0.1:43060", "Crane API base URL")
	cert := flag.String("tls-cert", "", "server certificate PEM")
	key := flag.String("tls-key", "", "server private key PEM")
	ca := flag.String("client-ca", "", "trusted CA for CA client certificates")
	flag.Parse()
	cfg := autoscaler.Config{ClusterID: os.Getenv("CRANE_ADAPTER_CLUSTER_ID")}
	client, err := autoscaler.NewHTTPClient(*craneURL, os.Getenv("CRANE_ADAPTER_TOKEN"), cfg.ClusterID)
	if err != nil {
		return err
	}
	service, err := autoscaler.NewServer(cfg, client)
	if err != nil {
		return err
	}
	opts, err := transportOptions(*addr, *cert, *key, *ca)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	tel, err := telemetry.New(ctx, telemetry.Config{ServiceName: "crane-autoscaler-adapter", ServiceVersion: version.Version})
	if err != nil {
		slog.Warn("telemetry using no-op providers", "error", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tel.Shutdown(shutdownCtx)
	}()
	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		return fmt.Errorf("adapter listener unavailable")
	}
	defer listener.Close()
	counter, err := tel.Meter.Int64Counter("crane.adapter.requests")
	if err != nil {
		return fmt.Errorf("initialize adapter request metric")
	}
	opts = append(opts, grpc.UnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		carrier := propagation.MapCarrier{}
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			for key, values := range md {
				if len(values) > 0 {
					carrier[key] = values[0]
				}
			}
		}
		ctx = otel.GetTextMapPropagator().Extract(ctx, carrier)
		ctx, span := tel.Tracer.Start(ctx, info.FullMethod)
		defer span.End()
		res, err := handler(ctx, req)
		counter.Add(ctx, 1, metric.WithAttributes(attribute.String("rpc.method", info.FullMethod), attribute.String("rpc.status", status.Code(err).String())))
		return res, err
	}))
	server := grpc.NewServer(opts...)
	pb.RegisterCloudProviderServer(server, service)
	go func() {
		<-ctx.Done()
		timer := time.AfterFunc(5*time.Second, server.Stop)
		server.GracefulStop()
		timer.Stop()
	}()
	slog.Info("autoscaler adapter serving", "cluster_id", cfg.ClusterID)
	return server.Serve(listener)
}

func transportOptions(addr, certPath, keyPath, caPath string) ([]grpc.ServerOption, error) {
	if certPath == "" && keyPath == "" && caPath == "" {
		host, _, err := net.SplitHostPort(addr)
		if err != nil || !net.ParseIP(host).IsLoopback() {
			return nil, fmt.Errorf("non-loopback gRPC listeners require a certificate, key, and client CA")
		}
		return nil, nil
	}
	if certPath == "" || keyPath == "" || caPath == "" {
		return nil, fmt.Errorf("all three mTLS files are required")
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("load adapter TLS identity")
	}
	data, err := os.ReadFile(caPath)
	if err != nil {
		return nil, fmt.Errorf("read adapter client CA")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(data) {
		return nil, fmt.Errorf("invalid client CA")
	}
	return []grpc.ServerOption{grpc.Creds(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12,
		Certificates: []tls.Certificate{cert}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots}))}, nil
}
