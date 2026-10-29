package awscompute

import (
	"strings"
	"testing"
)

func TestParseJoinCommandRequiresTrustedDiscovery(t *testing.T) {
	hash := "sha256:" + strings.Repeat("a", 64)
	command := "kubeadm join 10.80.0.10:6443 --token abcdef.0123456789abcdef --discovery-token-ca-cert-hash " + hash
	if _, _, _, err := parseJoinCommand(command, "https://10.80.0.10:6443", hash); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{command + " ; echo unsafe", strings.Replace(command, "10.80.0.10:6443", "10.80.0.11:6443", 1), strings.Replace(command, "abcdef.0123456789abcdef", "bad-token", 1)} {
		if _, _, _, err := parseJoinCommand(bad, "https://10.80.0.10:6443", hash); err == nil {
			t.Fatalf("accepted unsafe join command %q", bad)
		}
	}
}

func TestZoneNetworkScriptsSeparateClusterAndJoin(t *testing.T) {
	cilium, err := renderTemplate("cluster-cilium.sh.tmpl", scriptData{CiliumVersion: "1.19.0"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cilium, "helm upgrade --install cilium") || strings.Contains(cilium, "kubeadm init") {
		t.Fatal("invalid cluster network script")
	}
	join, err := renderTemplate("join-zone.sh.tmpl", scriptData{KubernetesVersion: "v1.35.0", JoinEndpoint: "10.80.0.10:6443", JoinToken: "abcdef.0123456789abcdef", JoinCAHash: "sha256:" + strings.Repeat("a", 64)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(join, "kubeadm join") || strings.Contains(join, "kubeadm init") {
		t.Fatal("invalid attachment join script")
	}
}
