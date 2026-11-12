package subd

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nabutabu/crane-oss/pkg/api"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func testJoinCA(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func workerJoinHostFixture(t *testing.T, in *api.ClusterJoinRequest, ca []byte, run func(context.Context, string, ...string) error) (LocalHost, string, string) {
	t.Helper()
	root := t.TempDir()
	etcDir := filepath.Join(root, "etc", "kubernetes")
	dataDir := filepath.Join(root, "var", "lib", "kubelet")
	joinDir := filepath.Join(root, "run")
	if err := os.MkdirAll(etcDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(joinDir, 0700); err != nil {
		t.Fatal(err)
	}
	bootstrapPath := filepath.Join(etcDir, "bootstrap-kubelet.conf")
	cfg := clientcmdapi.NewConfig()
	cfg.Clusters["destination"] = &clientcmdapi.Cluster{Server: in.APIServerEndpoint, CertificateAuthorityData: ca}
	cfg.AuthInfos["bootstrap"] = &clientcmdapi.AuthInfo{Token: "cccccc." + strings.Repeat("c", 16)}
	cfg.Contexts["destination"] = &clientcmdapi.Context{Cluster: "destination", AuthInfo: "bootstrap"}
	cfg.CurrentContext = "destination"
	data, err := clientcmd.Write(*cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bootstrapPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	in.CACertHash, err = api.KubernetesCAHash(ca)
	if err != nil {
		t.Fatal(err)
	}
	host := LocalHost{run: run, KubeletConfig: filepath.Join(etcDir, "kubelet.conf"), KubeletDataDir: dataDir, workerJoinDir: joinDir}
	return host, bootstrapPath, dataDir
}

func TestJoinClusterRerunsKubeadmForIncompleteBootstrap(t *testing.T) {
	in := workerJoinFixture()
	ca := testJoinCA(t)
	var commands []string
	host, bootstrapPath, dataDir := workerJoinHostFixture(t, &in, ca, func(ctx context.Context, command string, args ...string) error {
		commands = append(commands, command)
		if command == "kubeadm" {
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) <= 2*time.Minute {
				t.Fatal("kubeadm join did not receive the operation deadline")
			}
			if len(args) != 3 || args[0] != "join" || args[1] != "--config" {
				t.Fatalf("unexpected kubeadm arguments: %v", args)
			}
			data, err := os.ReadFile(args[2])
			if err != nil {
				t.Fatal(err)
			}
			var rendered struct {
				Discovery struct {
					BootstrapToken struct {
						Token string `json:"token"`
					} `json:"bootstrapToken"`
				} `json:"discovery"`
			}
			if err := json.Unmarshal(data, &rendered); err != nil || rendered.Discovery.BootstrapToken.Token != in.Token {
				t.Fatal("kubeadm join did not receive the current token")
			}
		}
		return nil
	})
	for _, name := range []string{"instance-config.yaml", "kubeadm-flags.env"} {
		if err := os.WriteFile(filepath.Join(dataDir, name), []byte("partial"), 0600); err != nil {
			t.Fatal(err)
		}
	}

	if err := host.JoinCluster(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if strings.Join(commands, ",") != "systemctl,kubeadm" {
		t.Fatalf("unexpected recovery commands: %v", commands)
	}
	for _, path := range []string{bootstrapPath, filepath.Join(dataDir, "instance-config.yaml"), filepath.Join(dataDir, "kubeadm-flags.env")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("incomplete join artifact remains at %s", path)
		}
	}
}

func TestJoinClusterFailsClosedForDifferentBootstrapCA(t *testing.T) {
	in := workerJoinFixture()
	ca := testJoinCA(t)
	wrongCA := testJoinCA(t)
	commands := 0
	host, bootstrapPath, _ := workerJoinHostFixture(t, &in, wrongCA, func(context.Context, string, ...string) error {
		commands++
		return nil
	})
	// Pin the requested cluster to a different CA than the existing bootstrap config.
	in.CACertHash, _ = api.KubernetesCAHash(ca)

	if err := host.JoinCluster(context.Background(), in); err == nil || !strings.Contains(err.Error(), "trust root conflicts") {
		t.Fatalf("expected a bootstrap trust conflict, got %v", err)
	}
	if commands != 0 {
		t.Fatalf("ran commands for conflicting bootstrap state: %d", commands)
	}
	if _, err := os.Stat(bootstrapPath); err != nil {
		t.Fatal("conflicting bootstrap file was removed")
	}
}

func TestJoinClusterReportsJoinDeadline(t *testing.T) {
	in := workerJoinFixture()
	ca := testJoinCA(t)
	host, _, _ := workerJoinHostFixture(t, &in, ca, func(_ context.Context, command string, _ ...string) error {
		if command == "kubeadm" {
			return context.DeadlineExceeded
		}
		return nil
	})
	// Exercise the first-boot path, where no interrupted bootstrap file exists.
	if err := os.Remove(filepath.Join(filepath.Dir(host.kubeletPath()), "bootstrap-kubelet.conf")); err != nil {
		t.Fatal(err)
	}
	err := host.JoinCluster(context.Background(), in)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected a timeout classification, got %v", err)
	}
}
