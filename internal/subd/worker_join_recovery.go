package subd

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/nabutabu/crane-oss/pkg/api"
	"k8s.io/client-go/tools/clientcmd"
)

// joinFile checks the file and every existing parent without following symlinks.
// These paths are root-owned; Subd serializes join requests under its state lock.
func joinFile(path string) (bool, error) {
	if !filepath.IsAbs(path) {
		return false, joinFailure("cleanup_validation")
	}
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, joinFailure("cleanup_validation")
		}
		if err == nil && !info.IsDir() {
			return false, joinFailure("cleanup_validation")
		}
		if parent == filepath.Dir(parent) {
			break
		}
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return false, joinFailure("cleanup_validation")
	}
	return true, nil
}

func (h LocalHost) prepareWorkerJoin(ctx context.Context, in api.ClusterJoinRequest) (bool, error) {
	etc := filepath.Dir(h.kubeletPath())
	bootstrap := filepath.Join(etc, "bootstrap-kubelet.conf")
	caPath := filepath.Join(etc, "pki", "ca.crt")
	paths := []string{bootstrap, filepath.Join(h.kubeletDataDir(), "instance-config.yaml"), filepath.Join(h.kubeletDataDir(), "kubeadm-flags.env"), caPath}
	present := make(map[string]bool)
	// Validate everything before stopping services or removing any artifact.
	for _, path := range append(append([]string{}, paths...), h.kubeletPath(), filepath.Join(h.kubeletDataDir(), "config.yaml")) {
		exists, err := joinFile(path)
		if err != nil {
			return false, err
		}
		present[path] = exists
	}
	if present[h.kubeletPath()] {
		return false, joinFailure("cleanup_validation")
	}
	if present[bootstrap] {
		cfg, err := clientcmd.LoadFromFile(bootstrap)
		if err != nil {
			return false, joinFailure("cleanup_validation")
		}
		current := cfg.Contexts[cfg.CurrentContext]
		if current == nil {
			return false, joinFailure("cleanup_validation")
		}
		cluster, auth := cfg.Clusters[current.Cluster], cfg.AuthInfos[current.AuthInfo]
		if cluster == nil || auth == nil || cluster.Server != in.APIServerEndpoint || cluster.InsecureSkipTLSVerify {
			return false, joinFailure("cleanup_validation")
		}
		ca := cluster.CertificateAuthorityData
		if len(ca) == 0 {
			if cluster.CertificateAuthority != caPath || !present[caPath] {
				return false, joinFailure("cleanup_validation")
			}
			ca, err = os.ReadFile(caPath)
			if err != nil {
				return false, joinFailure("cleanup_validation")
			}
		}
		pin, err := api.KubernetesCAHash(ca)
		if err != nil || pin != in.CACertHash {
			return false, joinFailure("cleanup_validation")
		}
	}
	if present[caPath] {
		ca, err := os.ReadFile(caPath)
		if err != nil {
			return false, joinFailure("cleanup_validation")
		}
		pin, err := api.KubernetesCAHash(ca)
		if err != nil || pin != in.CACertHash {
			return false, joinFailure("cleanup_validation")
		}
	}
	if present[filepath.Join(h.kubeletDataDir(), "config.yaml")] {
		if !present[bootstrap] {
			return false, joinFailure("cleanup_validation")
		}
		return true, nil
	}
	any := false
	for _, path := range paths {
		any = any || present[path]
	}
	if !any {
		return false, nil
	}
	// Flags alone do not establish which cluster created them.
	if !present[bootstrap] && !present[caPath] {
		return false, joinFailure("cleanup_validation")
	}
	for _, name := range []string{"admin.conf", "controller-manager.conf", "scheduler.conf", "pki/ca.key", "pki/apiserver.crt", "pki/apiserver.key"} {
		if _, err := os.Lstat(filepath.Join(etc, name)); !errors.Is(err, os.ErrNotExist) {
			return false, joinFailure("cleanup_validation")
		}
	}
	manifests := filepath.Join(etc, "manifests")
	if info, err := os.Lstat(manifests); err == nil {
		if !info.IsDir() {
			return false, joinFailure("cleanup_validation")
		}
		entries, err := os.ReadDir(manifests)
		if err != nil {
			return false, joinFailure("cleanup_validation")
		}
		for _, entry := range entries {
			if entry.Name() != ".kubelet-keep" {
				return false, joinFailure("cleanup_validation")
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, joinFailure("cleanup_validation")
	}
	run := h.run
	if run == nil {
		run = hostCommand
	}
	if err := run(ctx, "systemctl", "stop", "kubelet"); err != nil {
		return false, joinFailure("cleanup_execution")
	}
	// Remove bootstrap first and CA last. A failed/interrupted cleanup therefore
	// retains a trust anchor for the next attempt, even without a kubeconfig.
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return false, joinFailure("cleanup_execution")
		}
		exists, err := joinFile(path)
		if err != nil {
			return false, err
		}
		if exists {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return false, joinFailure("cleanup_execution")
			}
		}
	}
	return false, nil
}
