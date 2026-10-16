package awscompute

import (
	"bytes"
	"context"
	"github.com/nabutabu/crane-oss/internal/bootstrap"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSSMTransportPreservesScriptAndRedactsOutput(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "result")
	if err := os.WriteFile(filepath.Join(dir, "cloud-init"), []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/bash\nset -euo pipefail\nprintf '%s' \"literal '\\$HOME\" > " + shellQuote(marker) + "\n"
	var report bytes.Buffer
	aws := &fakeAWS{call: func(service, op string, in any) (any, error) {
		if service != "ssm" {
			t.Fatalf("unexpected distribution service %s", service)
		}
		switch op {
		case "send-command":
			input := in.(map[string]any)
			command := input["Parameters"].(map[string][]string)["commands"][0]
			command = strings.ReplaceAll(command, "/var/lib/crane-bootstrap.lock", filepath.Join(dir, "lock"))
			command = strings.ReplaceAll(command, "/var/lib/crane", filepath.Join(dir, "crane"))
			cmd := exec.Command("bash", "-c", command)
			cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("SSM command failed: %v: %s", err, out)
			}
			return map[string]any{"Command": map[string]string{"CommandId": "cmd-test"}}, nil
		case "get-command-invocation":
			return map[string]string{"Status": "Success", "StandardOutputContent": "password=fixture-secret"}, nil
		default:
			t.Fatalf("unexpected operation %s", op)
			return nil, nil
		}
	}}
	if err := runScript(context.Background(), aws, bootstrap.NewReporter(&report, false), "i-test", "test", script); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "literal '$HOME" {
		t.Fatalf("script quoting changed: %q", got)
	}
	if strings.Contains(report.String(), "fixture-secret") {
		t.Fatal("SSM output leaked credential")
	}
}

func TestAllBootstrapScriptsFitDirectSSMTransport(t *testing.T) {
	for name, script := range renderAll(t) {
		aws := &fakeAWS{call: func(_, op string, _ any) (any, error) {
			if op == "send-command" {
				return map[string]any{"Command": map[string]string{"CommandId": "cmd-test"}}, nil
			}
			return map[string]string{"Status": "Success"}, nil
		}}
		if err := runScript(context.Background(), aws, bootstrap.NewReporter(&bytes.Buffer{}, false), "i-test", name, script); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestBundleFetchPreservesExistingBundleOnFailure(t *testing.T) {
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("openssl not available")
	}
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	cmd := exec.Command("openssl", "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-256", "-nodes", "-subj", "/CN=test", "-days", "1", "-keyout", key, "-out", cert)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate fixture: %v: %s", err, out)
	}
	pem, err := os.ReadFile(cert)
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile("../../../golden_image_generator/templates/fetch-spire-bundle.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := strings.ReplaceAll(string(source), "/etc/crane", filepath.Join(dir, "etc"))
	helper := filepath.Join(dir, "fetch.sh")
	if err = os.WriteFile(helper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "aws"), []byte("#!/bin/sh\nprintf '%s' \"$TEST_BUNDLE\"\nexit \"$TEST_AWS_EXIT\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, bundle, status string
		success              bool
	}{
		{"initial", string(pem), "0", true}, {"denied", "", "1", false}, {"malformed", "not a certificate", "0", false}, {"refresh", string(pem), "0", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("bash", helper)
			cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "AWS_REGION=us-west-2", "SPIRE_BUNDLE_PARAMETER=/crane/test/spire-bundle", "TEST_BUNDLE="+tc.bundle, "TEST_AWS_EXIT="+tc.status)
			out, err := cmd.CombinedOutput()
			if (err == nil) != tc.success {
				t.Fatalf("unexpected fetch result: %v: %s", err, out)
			}
			got, err := os.ReadFile(filepath.Join(dir, "etc", "bundle.pem"))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(pem) {
				t.Fatal("fetch damaged trusted bundle")
			}
			matches, _ := filepath.Glob(filepath.Join(dir, "etc", ".bundle-*"))
			if len(matches) != 0 {
				t.Fatal("fetch left partial bundles")
			}
		})
	}
}
