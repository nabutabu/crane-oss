package awscompute

import (
	"strings"
	"testing"
)

func TestClusterNetworkScript(t *testing.T) {
	script, err := renderTemplate("cluster-cilium.sh.tmpl", scriptData{CiliumVersion: "1.19.0"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, "helm upgrade --install cilium") || strings.Contains(script, "kubeadm init") {
		t.Fatal("invalid cluster network script")
	}
}
