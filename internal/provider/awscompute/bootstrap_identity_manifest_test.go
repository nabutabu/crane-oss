package awscompute

import (
	"encoding/json"
	"io"
	"reflect"
	"regexp"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
)

func TestPlatformWorkloadIdentityManifests(t *testing.T) {
	script, err := RenderPlatformScript(testConfig(), testInfra(), testInfra().SpireAddress)
	if err != nil {
		t.Fatal(err)
	}
	deployments, accounts, roles := 0, 0, 0
	for _, block := range regexp.MustCompile(`(?s)kubectl apply -f - <<YAML\n(.*?)\nYAML`).FindAllStringSubmatch(script, -1) {
		decoder := yaml.NewYAMLOrJSONDecoder(strings.NewReader(block[1]), 4096)
		for {
			var raw json.RawMessage
			if err := decoder.Decode(&raw); err == io.EOF {
				break
			} else if err != nil {
				t.Fatal(err)
			}
			var meta struct{ Kind string }
			if err := json.Unmarshal(raw, &meta); err != nil {
				t.Fatal(err)
			}
			switch meta.Kind {
			case "Deployment":
				var d appsv1.Deployment
				if err := json.Unmarshal(raw, &d); err != nil {
					t.Fatal(err)
				}
				if d.Name != "crane-api" && d.Name != "dominator" {
					continue
				}
				deployments++
				pod := d.Spec.Template.Spec
				if d.Namespace != "crane-system" || pod.ServiceAccountName != d.Name || pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken {
					t.Fatal("incorrect workload service account")
				}
				want := &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchFields: []corev1.NodeSelectorRequirement{{Key: "metadata.name", Operator: corev1.NodeSelectorOpIn, Values: []string{"$NODE_NAME"}}}}}}}}
				if !reflect.DeepEqual(pod.Affinity, want) {
					t.Fatalf("incorrect platform placement: %+v", pod.Affinity)
				}
			case "ServiceAccount":
				var sa corev1.ServiceAccount
				if err := json.Unmarshal(raw, &sa); err != nil {
					t.Fatal(err)
				}
				accounts++
				if sa.Namespace != "crane-system" || (sa.Name != "crane-api" && sa.Name != "dominator") || sa.AutomountServiceAccountToken == nil || *sa.AutomountServiceAccountToken {
					t.Fatal("incorrect service account policy")
				}
			case "ClusterRole":
				var role rbacv1.ClusterRole
				if err := json.Unmarshal(raw, &role); err != nil {
					t.Fatal(err)
				}
				roles++
				var want []rbacv1.PolicyRule
				switch role.Name {
				case "crane-spire-attestor":
					want = []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"nodes/pods"}, ResourceNames: []string{"$NODE_NAME"}, Verbs: []string{"get"}}}
				case "crane-subd-kubernetes-operations":
					want = []rbacv1.PolicyRule{
						{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"get", "create", "update"}},
						{APIGroups: []string{"apps"}, Resources: []string{"deployments"}, Verbs: []string{"get", "update"}},
					}
				default:
					t.Fatalf("unexpected ClusterRole %s", role.Name)
				}
				if !reflect.DeepEqual(role.Rules, want) {
					t.Fatalf("incorrect %s permissions: %+v", role.Name, role.Rules)
				}
			case "ClusterRoleBinding":
				var b rbacv1.ClusterRoleBinding
				if err := json.Unmarshal(raw, &b); err != nil {
					t.Fatal(err)
				}
				if len(b.Subjects) != 1 || b.Subjects[0].Kind != "User" || b.RoleRef.Name != b.Name {
					t.Fatal("incorrect workload RBAC subject")
				}
				wantSubject := map[string]string{
					"crane-spire-attestor":             "crane-spire-attestor:$NODE_NAME",
					"crane-subd-kubernetes-operations": "crane-subd:$NODE_NAME",
				}[b.Name]
				if wantSubject == "" || b.Subjects[0].Name != wantSubject {
					t.Fatal("incorrect workload RBAC subject")
				}
			}
		}
	}
	if deployments != 2 || accounts != 2 || roles != 2 {
		t.Fatalf("deployments=%d accounts=%d roles=%d", deployments, accounts, roles)
	}
	activation := strings.Index(script, `/usr/local/bin/crane-configure-spire-k8s "$NODE_NAME"`)
	if activation < strings.Index(script, "kubeadm init") || activation > strings.Index(script, "kind: Deployment") {
		t.Fatal("workloads must deploy after Kubernetes attestor activation")
	}
}
