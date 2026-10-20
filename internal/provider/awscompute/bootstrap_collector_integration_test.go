package awscompute

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	collectorlog "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logpb "go.opentelemetry.io/proto/otlp/logs/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
)

// Run with CRANE_TEST_COLLECTOR=1 and Docker access. Uses the same pinned
// Collector image and merged config sources as the Kubernetes Deployment.
func TestCollectorBackendSecretIntegration(t *testing.T) {
	if os.Getenv("CRANE_TEST_COLLECTOR") != "1" {
		t.Skip("set CRANE_TEST_COLLECTOR=1 to run pinned Collector integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	delivered := make(chan struct{}, 1)
	backend := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-auth" || r.Header.Get("X-Backend-Key") != "synthetic-key" {
			t.Error("backend did not receive both custom authentication headers")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if r.URL.Path == "/tenant/example/v1/logs" && bytes.Contains(body, []byte("collector-backend-test")) {
			select {
			case delivered <- struct{}{}:
			default:
			}
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	backend.Listener.Close()
	backendListener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	backend.Listener = backendListener
	backend.StartTLS()
	defer backend.Close()
	backendHost := os.Getenv("CRANE_TEST_COLLECTOR_BACKEND_HOST")
	if backendHost == "" {
		backendHost = "host.docker.internal"
	}
	backendEndpoint := "https://" + net.JoinHostPort(backendHost, strconv.Itoa(backendListener.Addr().(*net.TCPAddr).Port))
	dir := t.TempDir()
	for _, sub := range []string{"tls", "exporter", "queue"} {
		if err := os.Mkdir(filepath.Join(dir, sub), 0700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(name string, content []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: backend.Certificate().Raw})
	key, err := x509.MarshalPKCS8PrivateKey(backend.TLS.Certificates[0].PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	write("tls/tls.crt", cert)
	write("tls/tls.key", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}))
	write("exporter/ca.crt", cert)
	fragment := "exporters:\n  otlp_http/backend:\n    endpoint: " + backendEndpoint + "/tenant/example\n    headers:\n      Authorization: Bearer synthetic-auth\n      X-Backend-Key: synthetic-key\n    compression: none\n    tls:\n      ca_file: /etc/otel-exporter/ca.crt\n      server_name_override: example.com\n"
	write("exporter/exporter.yaml", []byte(fragment))
	data, err := baseScriptData(testConfig(), testInfra())
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := renderTemplate("otel-collector.yaml.tmpl", data)
	if err != nil {
		t.Fatal(err)
	}
	// Extract the ConfigMap literal without introducing a new YAML dependency.
	parts := strings.SplitN(manifest, "  collector.yaml: |\n", 2)
	if len(parts) != 2 {
		t.Fatal("missing Collector configuration")
	}
	literal := strings.SplitN(parts[1], "\n---", 2)[0]
	lines := strings.Split(literal, "\n")
	for i := range lines {
		lines[i] = strings.TrimPrefix(lines[i], "    ")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	config := strings.Join(lines, "\n")
	write("collector.yaml", []byte(config))
	var image string
	for _, line := range strings.Split(manifest, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "image: ") {
			image = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "image: "))
		}
	}
	if image == "" {
		t.Fatal("missing pinned image")
	}
	baseArgs := []string{"run", "--rm", "--add-host", "host.docker.internal:host-gateway", "--user", strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid()),
		"--mount", "type=bind,src=" + filepath.Join(dir, "collector.yaml") + ",dst=/etc/otel/collector.yaml,readonly",
		"--mount", "type=bind,src=" + filepath.Join(dir, "tls") + ",dst=/etc/otel-tls,readonly",
		"--mount", "type=bind,src=" + filepath.Join(dir, "exporter") + ",dst=/etc/otel-exporter,readonly",
		"--mount", "type=bind,src=" + filepath.Join(dir, "queue") + ",dst=/var/lib/otelcol",
		"--env", "POD_NAME=collector-integration", "--env", "CRANE_OTLP_RECEIVER_TOKEN=synthetic-receiver-token"}
	configs := []string{"--config=/etc/otel/collector.yaml", "--config=/etc/otel-exporter/exporter.yaml"}
	validate := func(wantValid bool) {
		t.Helper()
		args := append(append(append([]string{}, baseArgs...), image, "validate"), configs...)
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if (err == nil) != wantValid {
			t.Fatalf("Collector configuration validation: %v, %s", err, out)
		}
		for _, secret := range []string{"synthetic-auth", "synthetic-key"} {
			if bytes.Contains(out, []byte(secret)) {
				t.Fatal("Collector validation leaked synthetic credentials")
			}
		}
	}
	validate(true)
	write("exporter/exporter.yaml", []byte("exporters:\n  otlp_http/backend:\n    headers: [invalid-header-map]\n"))
	validate(false)
	write("exporter/exporter.yaml", []byte(fragment))
	name := "crane-collector-test-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	args := append(append(append([]string{}, baseArgs...), "--publish", address+":4317", "--name", name, image), configs...)
	cmd := exec.CommandContext(ctx, "docker", args...)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stopCtx, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		_ = exec.CommandContext(stopCtx, "docker", "stop", "--time", "5", name).Run()
		_ = cmd.Wait()
		for _, secret := range []string{"synthetic-auth", "synthetic-key"} {
			if bytes.Contains(output.Bytes(), []byte(secret)) {
				t.Error("Collector runtime diagnostics leaked synthetic credentials")
			}
		}
		if t.Failed() {
			t.Log(output.String())
		}
	}()
	roots := x509.NewCertPool()
	roots.AddCert(backend.Certificate())
	dialCtx, stopDial := context.WithTimeout(ctx, 30*time.Second)
	defer stopDial()
	conn, err := grpc.DialContext(dialCtx, address, grpc.WithBlock(), grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12})))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	requestCtx := metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer synthetic-receiver-token"))
	_, err = collectorlog.NewLogsServiceClient(conn).Export(requestCtx, &collectorlog.ExportLogsServiceRequest{ResourceLogs: []*logpb.ResourceLogs{{ScopeLogs: []*logpb.ScopeLogs{{LogRecords: []*logpb.LogRecord{{Body: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "collector-backend-test"}}}}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-delivered:
	case <-ctx.Done():
		t.Fatal("authenticated backend did not receive log record")
	}
}
