package subd

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"github.com/nabutabu/crane-oss/pkg/api"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func (h LocalHost) kubeletPath() string {
	if h.KubeletConfig != "" {
		return h.KubeletConfig
	}
	return "/etc/kubernetes/kubelet.conf"
}

func (h LocalHost) kubeletDataDir() string {
	if h.KubeletDataDir != "" {
		return h.KubeletDataDir
	}
	return "/var/lib/kubelet"
}

func (h LocalHost) workerJoinConfigDir() string {
	if h.workerJoinDir != "" {
		return h.workerJoinDir
	}
	return "/run"
}

func removeIncompleteJoinFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("cannot inspect incomplete worker join file")
	}
	if err := os.Remove(path); err != nil {
		return errors.New("cannot clear incomplete worker join file")
	}
	return nil
}

func kubeletCredentials(path, endpoint, caHash string) (*rest.Config, tls.Certificate, error) {
	invalid := errors.New("local kubelet cluster identity does not match trusted discovery")
	cfg, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil || cfg.Host != endpoint || cfg.Insecure {
		return nil, tls.Certificate{}, invalid
	}
	ca := cfg.CAData
	if len(ca) == 0 {
		ca, err = os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, tls.Certificate{}, invalid
		}
	}
	hash, err := api.KubernetesCAHash(ca)
	if err != nil || hash != caHash {
		return nil, tls.Certificate{}, invalid
	}
	certData, keyData := cfg.CertData, cfg.KeyData
	if len(certData) == 0 {
		certData, err = os.ReadFile(cfg.CertFile)
		if err != nil {
			return nil, tls.Certificate{}, invalid
		}
	}
	if len(keyData) == 0 {
		keyData, err = os.ReadFile(cfg.KeyFile)
		if err != nil {
			return nil, tls.Certificate{}, invalid
		}
	}
	pair, err := tls.X509KeyPair(certData, keyData)
	if err != nil {
		return nil, tls.Certificate{}, invalid
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, tls.Certificate{}, invalid
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(ca)
	if _, err = cert.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return nil, tls.Certificate{}, invalid
	}
	cfg.Timeout = 15 * time.Second
	return cfg, pair, nil
}

func renderWorkerJoin(in api.ClusterJoinRequest) ([]byte, error) {
	if validateClusterJoin(in) != nil {
		return nil, errors.New("invalid worker join contract")
	}
	endpoint, _ := url.Parse(in.APIServerEndpoint)
	labels := api.SchedulingLabels(in.Registration.Scheduling, in.Registration.NodePoolID)
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	values := make([]string, 0, len(keys))
	for _, key := range keys {
		values = append(values, key+"="+labels[key])
	}
	taints := append([]api.SchedulingTaint{}, in.Registration.Scheduling.Taints...)
	taints = append(taints, api.SchedulingTaint{Key: api.ProvisioningTaint, Value: "pending", Effect: "NoSchedule"})
	return json.Marshal(map[string]any{"apiVersion": "kubeadm.k8s.io/v1beta4", "kind": "JoinConfiguration",
		"discovery":        map[string]any{"bootstrapToken": map[string]any{"apiServerEndpoint": endpoint.Host, "token": in.Token, "caCertHashes": []string{in.CACertHash}}},
		"nodeRegistration": map[string]any{"name": in.Registration.NodeName, "taints": taints, "kubeletExtraArgs": []map[string]string{{"name": "node-labels", "value": strings.Join(values, ",")}, {"name": "provider-id", "value": in.Registration.ProviderID}}}})
}

func (h LocalHost) JoinCluster(ctx context.Context, in api.ClusterJoinRequest) error {
	ctx, cancel := context.WithTimeout(ctx, api.OperationTimeout)
	defer cancel()

	config, err := renderWorkerJoin(in)
	if err != nil {
		return err
	}
	if _, err = os.Stat(h.kubeletPath()); err == nil {
		_, cert, err := kubeletCredentials(h.kubeletPath(), in.APIServerEndpoint, in.CACertHash)
		if err != nil {
			return err
		}
		leaf, err := x509.ParseCertificate(cert.Certificate[0])
		if err != nil || leaf.Subject.CommonName != "system:node:"+in.Registration.NodeName {
			return errors.New("existing kubelet belongs to another host")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("cannot inspect existing kubelet identity")
	}
	// A prior kubeadm attempt may have left a bootstrap kubeconfig. Only resume
	// kubelet when kubeadm's main configuration was also written. Without it,
	// restart kubeadm after clearing the narrow set of generated bootstrap files.
	bootstrapPath := filepath.Join(filepath.Dir(h.kubeletPath()), "bootstrap-kubelet.conf")
	if _, err := os.Stat(bootstrapPath); err == nil {
		cfg, err := clientcmd.LoadFromFile(bootstrapPath)
		if err != nil {
			return errors.New("cannot inspect interrupted worker join")
		}
		current := cfg.Contexts[cfg.CurrentContext]
		if current == nil {
			return errors.New("invalid interrupted worker join")
		}
		cluster := cfg.Clusters[current.Cluster]
		auth := cfg.AuthInfos[current.AuthInfo]
		if cluster == nil || auth == nil || cluster.Server != in.APIServerEndpoint || cluster.InsecureSkipTLSVerify {
			return errors.New("interrupted worker join conflicts with destination")
		}
		ca := cluster.CertificateAuthorityData
		if len(ca) == 0 {
			ca, err = os.ReadFile(cluster.CertificateAuthority)
			if err != nil {
				return errors.New("cannot inspect bootstrap trust root")
			}
		}
		pin, err := api.KubernetesCAHash(ca)
		if err != nil || pin != in.CACertHash {
			return errors.New("bootstrap trust root conflicts with destination")
		}
		run := h.run
		if run == nil {
			run = hostCommand
		}
		kubeletConfigPath := filepath.Join(h.kubeletDataDir(), "config.yaml")
		if _, err := os.Stat(kubeletConfigPath); errors.Is(err, os.ErrNotExist) {
			if run(ctx, "systemctl", "stop", "kubelet") != nil {
				return errors.New("cannot stop incomplete worker kubelet")
			}
			// kubelet.conf was verified absent above and the bootstrap config is
			// pinned to this destination. Remove only kubeadm's bootstrap
			// kubeconfig and generated kubelet config/flags before retrying join.
			for _, path := range []string{
				bootstrapPath,
				filepath.Join(h.kubeletDataDir(), "instance-config.yaml"),
				filepath.Join(h.kubeletDataDir(), "kubeadm-flags.env"),
			} {
				if err := removeIncompleteJoinFile(path); err != nil {
					return err
				}
			}
		} else if err != nil {
			return errors.New("cannot inspect kubelet bootstrap configuration")
		} else {
			auth.Token = in.Token
			auth.TokenFile = ""
			data, err := clientcmd.Write(*cfg)
			if err != nil {
				return errors.New("cannot refresh bootstrap credentials")
			}
			file, err := os.CreateTemp(filepath.Dir(bootstrapPath), ".crane-bootstrap-*")
			if err != nil {
				return errors.New("cannot stage bootstrap retry")
			}
			defer os.Remove(file.Name())
			if err = file.Chmod(0600); err == nil {
				_, err = file.Write(data)
			}
			closeErr := file.Close()
			if err != nil || closeErr != nil || os.Rename(file.Name(), bootstrapPath) != nil {
				return errors.New("cannot persist bootstrap retry")
			}
			if run(ctx, "systemctl", "restart", "kubelet") != nil {
				return errors.New("cannot resume worker kubelet")
			}
			if wait.PollUntilContextTimeout(ctx, time.Second, time.Minute, true, func(context.Context) (bool, error) {
				_, cert, err := kubeletCredentials(h.kubeletPath(), in.APIServerEndpoint, in.CACertHash)
				if err != nil {
					return false, nil
				}
				leaf, err := x509.ParseCertificate(cert.Certificate[0])
				return err == nil && leaf.Subject.CommonName == "system:node:"+in.Registration.NodeName, nil
			}) != nil {
				return errors.New("worker join retry is not complete")
			}
			return nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("cannot inspect bootstrap retry")
	}
	file, err := os.CreateTemp(h.workerJoinConfigDir(), "crane-worker-join-*.json")
	if err != nil {
		return errors.New("cannot stage worker join")
	}
	defer os.Remove(file.Name())
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(config)
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return errors.New("cannot protect worker join credentials")
	}
	run := h.run
	if run == nil {
		run = hostJoinCommand
	}
	if err := run(ctx, "kubeadm", "join", "--config", file.Name()); err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return errors.New("Kubernetes worker join timed out")
		}
		return errors.New("Kubernetes worker join failed")
	}
	return nil
}

type membershipHost interface {
	ClusterMembership(context.Context, api.ClusterJoinRequest, string) (api.ClusterMembership, error)
}

func (h LocalHost) ClusterMembership(ctx context.Context, in api.ClusterJoinRequest, challenge string) (api.ClusterMembership, error) {
	invalid := errors.New("cannot verify local cluster membership")
	cfg, pair, err := kubeletCredentials(h.kubeletPath(), in.APIServerEndpoint, in.CACertHash)
	if err != nil {
		return api.ClusterMembership{}, invalid
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || cert.Subject.CommonName != "system:node:"+in.Registration.NodeName {
		return api.ClusterMembership{}, invalid
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return api.ClusterMembership{}, invalid
	}
	node, err := client.CoreV1().Nodes().Get(ctx, in.Registration.NodeName, metav1.GetOptions{})
	if err != nil || node.UID == "" {
		return api.ClusterMembership{}, invalid
	}
	result := api.ClusterMembership{ContractHash: api.JoinFingerprint(in), NodeName: node.Name, NodeUID: string(node.UID), Certificate: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: pair.Certificate[0]})}
	sum := sha256.Sum256(api.MembershipProofData(challenge, result))
	signer, ok := pair.PrivateKey.(crypto.Signer)
	if !ok {
		return api.ClusterMembership{}, invalid
	}
	opts := crypto.SignerOpts(crypto.SHA256)
	if cert.PublicKeyAlgorithm == x509.Ed25519 {
		opts = crypto.Hash(0)
	}
	result.Signature, err = signer.Sign(rand.Reader, sum[:], opts)
	if err != nil {
		return api.ClusterMembership{}, invalid
	}
	return result, nil
}
