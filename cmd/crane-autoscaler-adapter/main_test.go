package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/nabutabu/crane-oss/internal/adapter/autoscaler/protos"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

func TestListenerRequiresMutualTLSOutsideLoopback(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:50051", ":50051", "10.0.0.1:50051", "localhost:50051"} {
		if _, err := transportOptions(addr, "", "", ""); err == nil {
			t.Fatalf("unprotected listener accepted: %s", addr)
		}
	}
	if _, err := transportOptions("127.0.0.1:50051", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := transportOptions("127.0.0.1:50051", "cert", "", ""); err == nil {
		t.Fatal("partial TLS accepted")
	}
}

func TestMutualTLSAcceptsTrustedClientAndRejectsMissingIdentity(t *testing.T) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "adapter-test-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	issue := func(serial int64, usage x509.ExtKeyUsage) ([]byte, []byte) {
		t.Helper()
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		template := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "adapter-test"}, DNSNames: []string{"localhost"},
			NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{usage}, KeyUsage: x509.KeyUsageDigitalSignature}
		der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		keyDER, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	}
	serverCert, serverKey := issue(2, x509.ExtKeyUsageServerAuth)
	clientCert, clientKey := issue(3, x509.ExtKeyUsageClientAuth)
	dir := t.TempDir()
	for name, data := range map[string][]byte{"server.pem": serverCert, "server.key": serverKey, "ca.pem": caPEM} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	opts, err := transportOptions("0.0.0.0:50051", filepath.Join(dir, "server.pem"), filepath.Join(dir, "server.key"), filepath.Join(dir, "ca.pem"))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(opts...)
	pb.RegisterCloudProviderServer(server, &pb.UnimplementedCloudProviderServer{})
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(caPEM)
	identity, err := tls.X509KeyPair(clientCert, clientKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, trusted := range []bool{true, false} {
		cfg := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: "localhost"}
		if trusted {
			cfg.Certificates = []tls.Certificate{identity}
		}
		conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(credentials.NewTLS(cfg)))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, callErr := pb.NewCloudProviderClient(conn).NodeGroups(ctx, &pb.NodeGroupsRequest{})
		cancel()
		conn.Close()
		if trusted && status.Code(callErr) != codes.Unimplemented {
			t.Fatalf("trusted client rejected: %v", callErr)
		}
		if !trusted && status.Code(callErr) != codes.Unavailable && status.Code(callErr) != codes.DeadlineExceeded {
			t.Fatalf("unauthenticated client accepted: %v", callErr)
		}
	}
}
