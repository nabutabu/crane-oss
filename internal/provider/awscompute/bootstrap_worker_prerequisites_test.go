package awscompute

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	core "k8s.io/api/core/v1"
	rbac "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/klog/v2"
)

func TestWorkerBootstrapDiscoveryPermissions(t *testing.T) {
	for _, name := range []string{"platform.sh.tmpl", "cluster-control-plane.sh.tmpl"} {
		t.Run(name, func(t *testing.T) {
			script, err := renderTemplate(name, scriptData{})
			if err != nil {
				t.Fatal(err)
			}
			start := strings.Index(script, "# Crane uses a dedicated bootstrap-token group")
			if start < 0 {
				t.Fatal("missing worker bootstrap permissions")
			}
			end := strings.Index(script[start:], "\nYAML\n")
			if end < 0 {
				t.Fatal("unterminated RBAC manifest")
			}
			section := script[start : start+end+len("\nYAML\n")]
			dir := t.TempDir()
			manifest := filepath.Join(dir, "rbac.yaml")
			// Exercise the rendered shell and its quoted heredoc twice. Applying stable
			// identities must produce the same documents, without create-only commands.
			stub := "#!/bin/sh\nset -eu\ntest \"$*\" = 'apply -f -'\ncat > \"$TEST_MANIFEST\"\n"
			if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(stub), 0700); err != nil {
				t.Fatal(err)
			}
			var first []byte
			for attempt := 0; attempt < 2; attempt++ {
				cmd := exec.Command("bash", "-eu", "-c", section)
				cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "TEST_MANIFEST="+manifest)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("RBAC apply failed: %v %s", err, out)
				}
				data, err := os.ReadFile(manifest)
				if err != nil {
					t.Fatal(err)
				}
				if attempt == 0 {
					first = data
				} else if !bytes.Equal(first, data) {
					t.Fatal("bootstrap retry changed RBAC identities")
				}
			}
			decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(first), 4096)
			seen := map[string]bool{}
			group := rbac.Subject{Kind: "Group", Name: "system:bootstrappers:crane", APIGroup: rbac.GroupName}
			for {
				var raw json.RawMessage
				if err := decoder.Decode(&raw); err == io.EOF {
					break
				} else if err != nil {
					t.Fatal(err)
				}
				var kind struct{ Kind string }
				if err := json.Unmarshal(raw, &kind); err != nil {
					t.Fatal(err)
				}
				if seen[kind.Kind] {
					t.Fatalf("duplicate RBAC kind %s", kind.Kind)
				}
				seen[kind.Kind] = true
				switch kind.Kind {
				case "Role":
					var role rbac.Role
					if err := json.Unmarshal(raw, &role); err != nil {
						t.Fatal(err)
					}
					expected := []rbac.PolicyRule{{APIGroups: []string{""}, Resources: []string{"configmaps"}, ResourceNames: []string{"kubeadm-config", "kubelet-config"}, Verbs: []string{"get"}}}
					if role.Name != "crane-worker-bootstrap-config" || role.Namespace != "kube-system" || !reflect.DeepEqual(role.Rules, expected) {
						t.Fatalf("bootstrap configuration access widened: %+v", role)
					}
				case "RoleBinding":
					var binding rbac.RoleBinding
					if err := json.Unmarshal(raw, &binding); err != nil {
						t.Fatal(err)
					}
					if binding.Name != "crane-worker-bootstrap-config" || binding.Namespace != "kube-system" || !reflect.DeepEqual(binding.Subjects, []rbac.Subject{group}) || binding.RoleRef != (rbac.RoleRef{APIGroup: rbac.GroupName, Kind: "Role", Name: "crane-worker-bootstrap-config"}) {
						t.Fatalf("incorrect configuration binding: %+v", binding)
					}
				case "ClusterRoleBinding":
					var binding rbac.ClusterRoleBinding
					if err := json.Unmarshal(raw, &binding); err != nil {
						t.Fatal(err)
					}
					// kubeadm:get-nodes grants only nodes/get. Do not bind Crane's tokens to
					// the default bootstrap CSR auto-approval roles or broader node roles.
					if binding.Name != "crane-bootstrap-node-discovery" || !reflect.DeepEqual(binding.Subjects, []rbac.Subject{group}) || binding.RoleRef != (rbac.RoleRef{APIGroup: rbac.GroupName, Kind: "ClusterRole", Name: "kubeadm:get-nodes"}) {
						t.Fatalf("incorrect node lookup binding: %+v", binding)
					}
				default:
					t.Fatalf("unexpected bootstrap permission resource %s", kind.Kind)
				}
			}
			if len(seen) != 3 {
				t.Fatalf("missing RBAC resources: %v", seen)
			}
		})
	}
}

func TestPlatformCiliumAgentToleratesWorkerAndStartupQuarantine(t *testing.T) {
	script, err := RenderPlatformScript(testConfig(), testInfra(), testInfra().SpireAddress)
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(script, "helm upgrade --install cilium")
	if start < 0 {
		t.Fatal("missing Cilium install")
	}
	end := strings.Index(script[start:], "--wait --timeout 10m")
	if end < 0 {
		t.Fatal("missing Helm readiness gate")
	}
	command := script[start : start+end+len("--wait --timeout 10m")]
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	stub := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$TEST_HELM_ARGS\"\n"
	if err := os.WriteFile(filepath.Join(dir, "helm"), []byte(stub), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-eu", "-c", command)
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "TEST_HELM_ARGS="+argsFile, "CILIUM_VERSION=1.19.0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Helm command failed: %v %s", err, out)
	}
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSpace(string(data)), "\n")
	values := map[string]string{}
	for i := 0; i < len(args); i++ {
		if args[i] == "--set" {
			i++
			if i == len(args) {
				t.Fatal("missing Helm value")
			}
			k, v, ok := strings.Cut(args[i], "=")
			if !ok {
				t.Fatal("invalid Helm value")
			}
			values[k] = v
		}
	}
	var tolerations []core.Toleration
	for _, index := range []string{"0", "1", "2", "3"} {
		prefix := "tolerations[" + index + "]."
		tolerations = append(tolerations, core.Toleration{Key: values[prefix+"key"], Operator: core.TolerationOperator(values[prefix+"operator"]), Value: values[prefix+"value"], Effect: core.TaintEffect(values[prefix+"effect"])})
	}
	for _, tc := range []struct {
		taint   core.Taint
		allowed bool
	}{
		{core.Taint{Key: "node-role.kubernetes.io/control-plane", Effect: core.TaintEffectNoSchedule}, true},
		{core.Taint{Key: "node.kubernetes.io/not-ready", Effect: core.TaintEffectNoSchedule}, true},
		{core.Taint{Key: "platform.crane.io/provisioning", Value: "pending", Effect: core.TaintEffectNoSchedule}, true},
		{core.Taint{Key: "node.cilium.io/agent-not-ready", Effect: core.TaintEffectNoSchedule}, true},
		{core.Taint{Key: "platform.crane.io/provisioning", Value: "other", Effect: core.TaintEffectNoSchedule}, false},
		{core.Taint{Key: "dedicated", Value: "other", Effect: core.TaintEffectNoSchedule}, false},
	} {
		allowed := false
		for _, tol := range tolerations {
			allowed = allowed || tol.ToleratesTaint(klog.Background(), &tc.taint, false)
		}
		if allowed != tc.allowed {
			t.Errorf("taint %+v: tolerated=%v, want %v", tc.taint, allowed, tc.allowed)
		}
	}
	for i, key := range []string{"node-role.kubernetes.io/control-plane", "node.kubernetes.io/not-ready"} {
		prefix := "operator.tolerations[" + strconv.Itoa(i) + "]."
		if values[prefix+"key"] != key || values[prefix+"operator"] != "Exists" || values[prefix+"effect"] != "NoSchedule" {
			t.Fatal("Cilium operator lost existing tolerations")
		}
	}
}
