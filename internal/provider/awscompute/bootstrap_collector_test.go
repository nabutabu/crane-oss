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
				if err == nil || !strings.Contains(string(output), "then rerun bootstrap with the same deployment state") || strings.Contains(string(output), "handoff-complete") {
					t.Fatalf("missing configuration did not stop with recovery instructions: %v, %s", err, output)
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
