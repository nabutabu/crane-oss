package subd

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"log/slog"
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
	if err := os.MkdirAll(filepath.Join(etcDir, "pki"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(etcDir, "pki", "ca.crt"), ca, 0600); err != nil {
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
			// Match kubeadm's real preflight blocker, rather than just accepting
			// the command after an incomplete cleanup.
			var configPath string
			for i, arg := range args {
				if arg == "--config" {
					configPath = args[i+1]
				}
			}
			if configPath == "" {
				t.Fatal("missing join config")
			}
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

	if err := host.JoinCluster(context.Background(), in); err == nil || !strings.Contains(err.Error(), "cleanup_validation") {
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

func writeJoinFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestJoinClusterRecoversEveryCleanupInterruption(t *testing.T) {
	// Simulate a process crash after each deletion in the documented order.
	for removed := 0; removed <= 4; removed++ {
		t.Run(string(rune('0'+removed)), func(t *testing.T) {
			in := workerJoinFixture()
			ca := testJoinCA(t)
			h, bootstrap, dataDir := workerJoinHostFixture(t, &in, ca, nil)
			paths := []string{bootstrap, filepath.Join(dataDir, "instance-config.yaml"), filepath.Join(dataDir, "kubeadm-flags.env"), filepath.Join(filepath.Dir(bootstrap), "pki", "ca.crt")}
			for _, path := range paths[1:3] {
				writeJoinFile(t, path, []byte("partial"))
			}
			for _, path := range paths[:removed] {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			joined := 0
			h.run = func(_ context.Context, command string, args ...string) error {
				if command == "systemctl" && strings.Join(args, " ") != "stop kubelet" {
					t.Fatalf("unexpected command %s %v", command, args)
				}
				if command == "kubeadm" {
					for _, path := range paths {
						if _, err := os.Lstat(path); !os.IsNotExist(err) {
							t.Fatalf("kubeadm preflight would reject remaining artifact %s: %v", path, err)
						}
					}
					joined++
				}
				return nil
			}
			if err := h.JoinCluster(context.Background(), in); err != nil {
				t.Fatal(err)
			}
			if joined != 1 {
				t.Fatalf("join calls = %d", joined)
			}
		})
	}
}

func TestJoinClusterRejectsUnsafeCleanupWithoutMutations(t *testing.T) {
	for _, scenario := range []string{"wrong-ca", "malformed-ca", "symlink-ca", "dangling-bootstrap", "directory-flags", "symlink-parent", "admin", "ca-key", "manifest", "wrong-endpoint", "malformed-bootstrap", "orphan-config"} {
		t.Run(scenario, func(t *testing.T) {
			in := workerJoinFixture()
			ca := testJoinCA(t)
			calls := 0
			h, bootstrap, dataDir := workerJoinHostFixture(t, &in, ca, func(context.Context, string, ...string) error { calls++; return nil })
			caPath := filepath.Join(filepath.Dir(bootstrap), "pki", "ca.crt")
			switch scenario {
			case "wrong-ca":
				writeJoinFile(t, caPath, testJoinCA(t))
			case "malformed-ca":
				writeJoinFile(t, caPath, []byte("invalid"))
			case "symlink-ca":
				target := filepath.Join(t.TempDir(), "public-ca")
				writeJoinFile(t, target, ca)
				if err := os.Remove(caPath); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, caPath); err != nil {
					t.Fatal(err)
				}
			case "dangling-bootstrap":
				if err := os.Remove(bootstrap); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("missing", bootstrap); err != nil {
					t.Fatal(err)
				}
			case "directory-flags":
				if err := os.Mkdir(filepath.Join(dataDir, "kubeadm-flags.env"), 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink-parent":
				pki := filepath.Dir(caPath)
				moved := filepath.Join(t.TempDir(), "pki")
				if err := os.Rename(pki, moved); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(moved, pki); err != nil {
					t.Fatal(err)
				}
			case "admin":
				writeJoinFile(t, filepath.Join(filepath.Dir(bootstrap), "admin.conf"), []byte("protected"))
			case "ca-key":
				writeJoinFile(t, filepath.Join(filepath.Dir(caPath), "ca.key"), []byte("protected"))
			case "manifest":
				writeJoinFile(t, filepath.Join(filepath.Dir(bootstrap), "manifests", "kube-apiserver.yaml"), []byte("protected"))
			case "wrong-endpoint":
				cfg, err := clientcmd.LoadFromFile(bootstrap)
				if err != nil {
					t.Fatal(err)
				}
				cfg.Clusters["destination"].Server = "https://10.0.0.99:6443"
				data, err := clientcmd.Write(*cfg)
				if err != nil {
					t.Fatal(err)
				}
				writeJoinFile(t, bootstrap, data)
			case "malformed-bootstrap":
				writeJoinFile(t, bootstrap, []byte("[invalid"))
			case "orphan-config":
				if err := os.Remove(bootstrap); err != nil {
					t.Fatal(err)
				}
				writeJoinFile(t, filepath.Join(dataDir, "config.yaml"), []byte("configured"))
			}
			before, err := os.ReadFile(caPath)
			if err != nil {
				t.Fatal(err)
			}
			err = h.JoinCluster(context.Background(), in)
			if workerJoinCategory(err) != "cleanup_validation" || calls != 0 {
				t.Fatalf("unsafe cleanup: err=%v calls=%d", err, calls)
			}
			after, err := os.ReadFile(caPath)
			if err != nil || string(before) != string(after) {
				t.Fatal("CA changed on validation failure")
			}
			if scenario != "orphan-config" {
				if _, err := os.Lstat(bootstrap); err != nil {
					t.Fatal("bootstrap removed before validation completed")
				}
			}
		})
	}
}

func TestJoinClusterStopFailurePreservesArtifacts(t *testing.T) {
	in := workerJoinFixture()
	ca := testJoinCA(t)
	h, bootstrap, _ := workerJoinHostFixture(t, &in, ca, func(context.Context, string, ...string) error { return errors.New("private failure") })
	if err := h.JoinCluster(context.Background(), in); workerJoinCategory(err) != "cleanup_execution" {
		t.Fatal(err)
	}
	for _, path := range []string{bootstrap, filepath.Join(filepath.Dir(bootstrap), "pki", "ca.crt")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal("artifact removed after stop failure")
		}
	}
}

func TestJoinClusterFailedAttemptCanRetry(t *testing.T) {
	in := workerJoinFixture()
	ca := testJoinCA(t)
	h, bootstrap, _ := workerJoinHostFixture(t, &in, ca, nil)
	attempts := 0
	h.run = func(_ context.Context, command string, _ ...string) error {
		if command != "kubeadm" {
			return nil
		}
		attempts++
		caPath := filepath.Join(filepath.Dir(bootstrap), "pki", "ca.crt")
		if _, err := os.Stat(caPath); !os.IsNotExist(err) {
			t.Fatal("CA blocks kubeadm preflight")
		}
		if attempts == 1 {
			writeJoinFile(t, caPath, ca)
			return errors.New("private kubeadm failure")
		}
		return nil
	}
	if err := h.JoinCluster(context.Background(), in); err == nil {
		t.Fatal("expected interrupted first join")
	}
	in.Fence++
	in.Token = "bbbbbb." + strings.Repeat("b", 16)
	if err := h.JoinCluster(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatal(attempts)
	}
}

func TestJoinClusterResumesConfiguredKubeletAndPreservesCompletedJoin(t *testing.T) {
	in := workerJoinFixture()
	// A test-only CA/client pair makes the real kubelet identity validation run.
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "system:node:" + in.Registration.NodeName, Organization: []string{"system:nodes"}}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	h, bootstrap, dataDir := workerJoinHostFixture(t, &in, ca, nil)
	writeJoinFile(t, filepath.Join(dataDir, "config.yaml"), []byte("configured"))
	cfg, err := clientcmd.LoadFromFile(bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AuthInfos["bootstrap"] = &clientcmdapi.AuthInfo{ClientCertificateData: ca, ClientKeyData: pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})}
	credentials, err := clientcmd.Write(*cfg)
	if err != nil {
		t.Fatal(err)
	}
	restarts := 0
	h.run = func(_ context.Context, command string, args ...string) error {
		if command != "systemctl" || strings.Join(args, " ") != "restart kubelet" {
			t.Fatalf("unexpected command %s %v", command, args)
		}
		refreshed, err := clientcmd.LoadFromFile(bootstrap)
		if err != nil {
			t.Fatal(err)
		}
		if refreshed.AuthInfos["bootstrap"].Token != in.Token {
			t.Fatal("resume lost fresh token")
		}
		writeJoinFile(t, h.kubeletPath(), credentials)
		restarts++
		return nil
	}
	if err := h.JoinCluster(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if restarts != 1 {
		t.Fatal(restarts)
	}
	caPath := filepath.Join(filepath.Dir(bootstrap), "pki", "ca.crt")
	if b, err := os.ReadFile(caPath); err != nil || string(b) != string(ca) {
		t.Fatal("resume changed CA")
	}
	h.run = func(context.Context, string, ...string) error { t.Fatal("completed join executed command"); return nil }
	if err := h.JoinCluster(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	in.Registration.HostID = "different-host"
	in.Registration.NodeName = api.WorkerNodeName(in.Registration.HostID)
	if err := h.JoinCluster(context.Background(), in); err == nil {
		t.Fatal("accepted another host's completed identity")
	}
}

func TestJoinCleanupCancellationAndFilesystemChange(t *testing.T) {
	for _, mode := range []string{"cancel", "filesystem-change"} {
		t.Run(mode, func(t *testing.T) {
			in := workerJoinFixture()
			ca := testJoinCA(t)
			h, bootstrap, dataDir := workerJoinHostFixture(t, &in, ca, nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			h.run = func(_ context.Context, cmd string, _ ...string) error {
				if cmd != "systemctl" {
					t.Fatal("kubeadm ran after cleanup failed")
				}
				if mode == "cancel" {
					cancel()
				} else {
					if err := os.Mkdir(filepath.Join(dataDir, "instance-config.yaml"), 0700); err != nil {
						t.Fatal(err)
					}
				}
				return nil
			}
			if err := h.JoinCluster(ctx, in); err == nil {
				t.Fatal("cleanup failure ignored")
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(bootstrap), "pki", "ca.crt")); err != nil {
				t.Fatal("cleanup lost its final trust anchor")
			}
		})
	}
}

func TestWorkerJoinFailureLogContainsOnlySafeCategory(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(old)
	in := workerJoinFixture()
	ca := testJoinCA(t)
	h, _, _ := workerJoinHostFixture(t, &in, ca, func(_ context.Context, command string, _ ...string) error {
		if command == "kubeadm" {
			return errors.New(in.Token + " private-command-output")
		}
		return nil
	})
	if err := h.JoinCluster(context.Background(), in); workerJoinCategory(err) != "command_failed" {
		t.Fatal(err)
	}
	text := logs.String()
	if strings.Contains(text, in.Token) || strings.Contains(text, "private-command-output") || strings.Contains(text, in.APIServerEndpoint) {
		t.Fatal("private command data leaked")
	}
	if !strings.Contains(text, "command_failed") || !strings.Contains(text, in.OperationID) {
		t.Fatal("missing safe diagnostics")
	}
}
