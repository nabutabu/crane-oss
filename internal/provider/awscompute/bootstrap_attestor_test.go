package awscompute

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Exercise the image-installed helper with real client certificates and a TLS
// kubelet stand-in. Only systemd and retry sleeps are stubbed.
func TestSPIREKubernetesAttestorCredentials(t *testing.T) {
	if testing.Short() {
		t.Skip("real certificate and shell integration")
	}
	for _, tool := range []string{"openssl", "curl", "python3", "flock"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s unavailable", tool)
		}
	}
	if err := exec.Command("python3", "-c", "import yaml").Run(); err != nil {
		t.Skip("python3-yaml unavailable")
	}
	root := t.TempDir()
	write := func(name string, body []byte, mode os.FileMode) {
		t.Helper()
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, body, mode); err != nil {
			t.Fatal(err)
		}
	}
	certificate := func(name string) ([]byte, []byte) {
		t.Helper()
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		cert := &x509.Certificate{SerialNumber: big.NewInt(now.UnixNano()), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(48 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
		der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	}
	ca, key := certificate("cluster-ca")
	serving, servingKey := certificate("node.test")
	write("etc/kubernetes/pki/ca.crt", ca, 0600)
	write("etc/kubernetes/pki/ca.key", key, 0600)
	write("var/lib/kubelet/pki/kubelet.crt", serving, 0600)
	write("var/lib/kubelet/config.yaml", []byte("apiVersion: kubelet.config.k8s.io/v1beta1\nkind: KubeletConfiguration\n"), 0600)
	base, err := os.ReadFile("../../../golden_image_generator/templates/spire-agent.conf")
	if err != nil {
		t.Fatal(err)
	}
	write("etc/spire/agent-base.conf", base, 0600)
	write("etc/spire/agent.conf", base, 0600)
	write("bin/systemctl", []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$CALLS\"\nif [ -e \"$FAIL_SYSTEMD\" ]; then exit 1; fi\n"), 0700)
	write("bin/sleep", []byte("#!/bin/sh\nexit 0\n"), 0700)
	pair, err := tls.X509KeyPair(serving, servingKey)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca)
	var response atomic.Int32
	response.Store(http.StatusOK)
	var requests atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/pods" || r.TLS.PeerCertificates[0].Subject.CommonName != "crane-spire-attestor:node.test" {
			t.Error("incorrect kubelet request identity or path")
		}
		w.WriteHeader(int(response.Load()))
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool, MinVersion: tls.VersionTLS12}
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
	helper, err := os.ReadFile("../../../golden_image_generator/templates/configure-spire-k8s.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(helper)
	for _, prefix := range []string{"/var/lib/crane", "/var/lib/kubelet", "/etc/spire", "/etc/kubernetes"} {
		script = strings.ReplaceAll(script, prefix, root+prefix)
	}
	script = strings.ReplaceAll(script, ":10250", ":"+port)
	write("helper", []byte(script), 0700)
	run := func(configure bool, wantOK bool) {
		t.Helper()
		var args []string
		if configure {
			args = []string{"node.test"}
		}
		cmd := exec.Command(filepath.Join(root, "helper"), args...)
		cmd.Env = append(os.Environ(), "PATH="+filepath.Join(root, "bin")+":"+os.Getenv("PATH"), "CALLS="+filepath.Join(root, "calls"), "FAIL_SYSTEMD="+filepath.Join(root, "fail-systemd"))
		out, err := cmd.CombinedOutput()
		if (err == nil) != wantOK {
			t.Fatalf("helper success=%v want %v: %s", err == nil, wantOK, out)
		}
		if !wantOK && (!strings.Contains(string(out), "configuration failed") || strings.Contains(string(out), "PRIVATE KEY")) {
			t.Fatalf("unsanitized failure: %s", out)
		}
	}
	run(true, true)
	current := filepath.Join(root, "var/lib/crane/spire-k8s/current")
	first, err := os.ReadFile(filepath.Join(current, "client.crt"))
	if err != nil {
		t.Fatal(err)
	}
	conf, err := os.ReadFile(filepath.Join(root, "etc/spire/agent.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(conf), `WorkloadAttestor "k8s"`) || !strings.Contains(string(conf), `WorkloadAttestor "systemd"`) {
		t.Fatal("activation lost required attestors")
	}
	run(true, true)
	second, _ := os.ReadFile(filepath.Join(current, "client.crt"))
	if string(first) != string(second) {
		t.Fatal("bootstrap retry unnecessarily rotated credential")
	}
	before, _ := os.ReadFile(filepath.Join(root, "calls"))
	run(false, true)
	after, _ := os.ReadFile(filepath.Join(root, "calls"))
	if string(before) != string(after) {
		t.Fatal("renewal changed system services")
	}
	for _, name := range []string{"client.key", "client.crt", "kubelet-ca.pem"} {
		info, err := os.Stat(filepath.Join(current, name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("insecure credential permissions: %s", name)
		}
	}
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		response.Store(int32(status))
		old, _ := os.Readlink(current)
		run(false, false)
		next, _ := os.Readlink(current)
		if old != next {
			t.Fatal("failed authorization replaced valid credentials")
		}
	}
	response.Store(http.StatusOK)
	// TLS failure must retain the last known-good credential/trust generation.
	old, _ := os.Readlink(current)
	write("var/lib/kubelet/pki/kubelet.crt", ca, 0600)
	run(false, false)
	next, _ := os.Readlink(current)
	if next != old {
		t.Fatal("TLS failure replaced valid credentials")
	}
	write("var/lib/kubelet/pki/kubelet.crt", serving, 0600)
	// A broken local credential is replaced and verified before being published.
	write("var/lib/crane/spire-k8s/current/client.crt", []byte("invalid certificate"), 0600)
	run(false, true)
	renewed, _ := os.ReadFile(filepath.Join(current, "client.crt"))
	if string(renewed) == string(first) {
		t.Fatal("invalid credential not renewed")
	}
	// An interrupted activation must be restartable even after config was written.
	write("fail-systemd", nil, 0600)
	run(true, false)
	if err := os.Remove(filepath.Join(root, "fail-systemd")); err != nil {
		t.Fatal(err)
	}
	run(true, true)
	if requests.Load() < 5 {
		t.Fatal(fmt.Sprintf("too few authenticated kubelet requests: %d", requests.Load()))
	}
}
