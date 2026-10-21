package awscompute

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCollectorSecretHandoff(t *testing.T) {
	script := renderAll(t)["platform"]
	start := strings.Index(script, "# The deployment system owns backend configuration.")
	end := strings.Index(script, "readonly OTEL_STATE_DIR=")
	if start < 0 || end <= start {
		t.Fatal("missing Secret handoff")
	}
	gate := script[start:end]
	for _, state := range []string{"missing", "empty", "present", "access-error"} {
		t.Run(state, func(t *testing.T) {
			// Kubernetes returns only presence and metadata, never Secret data.
			harness := `set -euo pipefail
kubectl() {
 case "$*" in
  *go-template*)
   case "$SECRET_STATE" in missing|empty) return 0;; present) printf present;; access-error) return 1;; esac ;;
  *jsonpath*) printf 'object-uid:1';;
  *) return 99;;
 esac
}
` + gate + `printf 'handoff-complete:%s' "$EXPORTER_SECRET_VERSION"`
			cmd := exec.Command("bash", "-c", harness)
			cmd.Env = append(os.Environ(), "SECRET_STATE="+state)
			output, err := cmd.CombinedOutput()
			if state == "present" {
				if err != nil || string(output) != "handoff-complete:object-uid:1" {
					t.Fatalf("handoff failed: %v, %s", err, output)
				}
			} else if state == "access-error" {
				if err == nil || !strings.Contains(string(output), "Check Kubernetes access and availability") || strings.Contains(string(output), "create Secret") || strings.Contains(string(output), "handoff-complete") {
					t.Fatalf("access failure was misreported as missing configuration: %v, %s", err, output)
				}
			} else {
				if err != nil || string(output) != "handoff-complete:missing" {
					t.Fatalf("missing configuration blocked API handoff: %v, %s", err, output)
				}
			}
		})
	}
	for _, forbidden := range []string{"NEW_RELIC", "new-relic", "get secret otel-exporter-config -o yaml"} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("script contains backend credential handling: %s", forbidden)
		}
	}
}

func TestCollectorConfigVersionConvergesAndRotates(t *testing.T) {
	script := renderAll(t)["platform"]
	start := strings.Index(script, "OTEL_CONFIG_HASH=")
	if start < 0 {
		t.Fatal("missing Collector rollout configuration")
	}
	end := strings.Index(script[start:], "\nchmod ")
	if end < 0 {
		t.Fatal("missing Collector rollout configuration")
	}
	render := script[start:start+end] + `
printf '%s' "$OTEL_CONFIG_HASH"`
	dir := t.TempDir()
	for name, contents := range map[string]string{
		"otel-collector.node.yaml": "hash: REPLACE_OTEL_CONFIG_HASH\n",
		"receiver-token":           "synthetic-receiver-token", "tls.crt": "synthetic-certificate", "tls.key": "synthetic-private-key",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	hash := func(version string) string {
		t.Helper()
		// Temp directory names change between passes; unchanged contents must converge.
		tmp := t.TempDir()
		if err := os.WriteFile(filepath.Join(tmp, "headers"), []byte("synthetic-header"), 0600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("bash", "-euo", "pipefail", "-c", render)
		cmd.Env = append(os.Environ(), "CRANE_DIR="+dir, "OTEL_STATE_DIR="+dir, "OTEL_TMP_DIR="+tmp, "EXPORTER_SECRET_VERSION="+version)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("render Collector rollout: %v, %s", err, out)
		}
		return string(out)
	}
	first := hash("uid:1")
	if len(first) != 64 || hash("uid:1") != first {
		t.Fatal("unchanged rerun changes Collector rollout")
	}
	if hash("uid:2") == first || hash("replacement-uid:1") == first {
		t.Fatal("Secret rotation/replacement did not change rollout")
	}
	if err := os.WriteFile(filepath.Join(dir, "tls.crt"), []byte("rotated-certificate"), 0600); err != nil {
		t.Fatal(err)
	}
	if hash("uid:1") == first {
		t.Fatal("receiver rotation did not change rollout")
	}
}

func TestObservabilityReadinessIsSeparateFromCore(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq required")
	}
	scripts := renderAll(t)
	for _, name := range []string{"platform", "validate"} {
		if strings.Contains(scripts[name], "rollout status deployment/otel-collector") {
			t.Fatal("Collector readiness blocks core bootstrap")
		}
	}
	script := scripts["validate"]
	_, section, ok := strings.Cut(script, "# ---- Observability is reported separately from core zone readiness ---------")
	if !ok {
		t.Fatal("missing independent observability check")
	}
	section, _, ok = strings.Cut(section, "# ---- SPIRE workload attestation")
	if !ok {
		t.Fatal("missing core identity check")
	}
	for _, state := range []string{"missing", "ready", "failed", "access-error"} {
		t.Run(state, func(t *testing.T) {
			harness := `set -euo pipefail
kubectl(){
 case "$*" in
  *"get secret"*) case "$TEST_STATE" in missing) :;; access-error) return 1;; *) printf present;; esac;;
  *"get deployment"*)
   if [ "$TEST_STATE" = ready ]; then
    printf '%s' '{"metadata":{"generation":2},"spec":{"replicas":1},"status":{"observedGeneration":2,"updatedReplicas":1,"replicas":1,"availableReplicas":1}}'
   else printf '%s' '{"metadata":{"generation":2},"spec":{"replicas":1},"status":{}}'; fi;;
  *) return 99;;
 esac
}
` + section + `printf '%s' "$OBSERVABILITY_STATUS"`
			cmd := exec.Command("bash", "-c", harness)
			cmd.Env = append(os.Environ(), "TEST_STATE="+state)
			out, err := cmd.CombinedOutput()
			if state == "access-error" {
				if err == nil {
					t.Fatal("Kubernetes access error was hidden")
				}
				return
			}
			want := state
			if state == "missing" {
				want = "configuration_pending"
			}
			if err != nil || string(out) != want {
				t.Fatal("incorrect independent readiness", string(out), err)
			}
		})
	}
}
