package awscompute

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func testConfig() bootstrapConfig {
	return bootstrapConfig{
		Name:             "crane-platform",
		DevelopmentStage: "test", GroupSuffix: "general",
		AWS: AWSConfig{Region: "us-west-2", AccountID: "123456789012"},
		Network: NetworkConfig{
			VPCCIDR:            "10.80.0.0/16",
			PublicSubnetCIDRs:  []string{"10.80.0.0/24", "10.80.1.0/24"},
			PrivateSubnetCIDRs: []string{"10.80.16.0/24", "10.80.17.0/24"},
		},
		SPIRE: SPIREConfig{
			Version:      "1.9.3",
			InstanceType: "t3.small",
			StorageGiB:   20,
			TrustDomain:  "crane.internal",
		},
		Platform: PlatformConfig{
			CraneAPIImage:     "ghcr.io/nabutabu/crane-oss:0.2.0",
			DominatorImage:    "ghcr.io/nabutabu/crane-oss-dominator:0.2.0",
			InstanceType:      "t3.medium",
			StorageGiB:        30,
			KubernetesVersion: "v1.35.0",
			CiliumVersion:     "1.19.0",
			PodCIDR:           "10.244.0.0/16",
		},
		RDS: RDSConfig{
			Identifier:               "crane-platform-db",
			Engine:                   "postgres",
			InstanceClass:            "db.t3.micro",
			StorageGiB:               20,
			DeletionProtection:       true,
			ManageMasterUserPassword: true,
			MasterUsername:           "postgres",
			Port:                     5432,
		},
	}
}

func testInfra() Infrastructure {
	return Infrastructure{
		SpireAddress:                     "internal-crane-spire.elb.us-west-2.amazonaws.com",
		VPCID:                            "vpc-created",
		SpireLoadBalancerSecurityGroupID: "sg-spire-nlb",
		PrivateSubnetIDs:                 []string{"subnet-private-a", "subnet-private-b"},
		DBEndpoint:                       "crane-platform-db.abc123.us-west-2.rds.amazonaws.com",
		DBSecretARN:                      "arn:aws:secretsmanager:us-west-2:123456789012:secret:rds!db-abc123",
		RuntimeSecretARN:                 "arn:aws:secretsmanager:us-west-2:123456789012:secret:crane-platform/runtime-abc123",
		SpireSecurityGroupID:             "sg-spire",
		PlatformSecurityGroupID:          "sg-platform",
		PublicSubnetIDs:                  []string{"subnet-aaaa", "subnet-bbbb"},
		AvailabilityZones:                []string{"us-west-2a", "us-west-2b"},
	}
}

func testAgentHosts() []bootstrapAgentHost {
	return []bootstrapAgentHost{{ID: "i-11111111111111111", PrivateIP: "10.80.0.11", Role: "spire", AMI: "ami-11111111"}, {ID: "i-0abcdef1234567890", PrivateIP: "10.80.0.12", Role: "platform", AMI: "ami-22222222"}}
}

func renderAll(t *testing.T) map[string]string {
	t.Helper()
	cfg, infra := testConfig(), testInfra()
	spire, err := RenderSpireScript(cfg, infra)
	if err != nil {
		t.Fatalf("RenderSpireScript: %v", err)
	}
	identity, err := RenderIdentityScript(cfg, infra, "i-0abcdef1234567890", "i-11111111111111111")
	if err != nil {
		t.Fatalf("RenderIdentityScript: %v", err)
	}
	platform, err := RenderPlatformScript(cfg, infra, infra.SpireAddress)
	if err != nil {
		t.Fatalf("RenderPlatformScript: %v", err)
	}
	validate, err := RenderValidateScript(cfg, infra, testAgentHosts())
	if err != nil {
		t.Fatalf("RenderValidateScript: %v", err)
	}
	subdScript, err := RenderSubdScript(cfg, infra, testAgentHosts()[0])
	if err != nil {
		t.Fatal(err)
	}
	seed, err := RenderSeedHostsScript(cfg, infra, testAgentHosts())
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{
		"subd":       subdScript,
		"seed-hosts": seed,
		"spire":      spire,
		"identity":   identity,
		"platform":   platform,
		"validate":   validate,
	}
}

func TestRenderedScriptsCommonInvariants(t *testing.T) {
	for name, script := range renderAll(t) {
		if !strings.HasPrefix(script, "#!/usr/bin/env bash") {
			t.Errorf("%s: missing bash shebang", name)
		}
		if !strings.Contains(script, "set -euo pipefail") {
			t.Errorf("%s: missing strict mode", name)
		}
		if strings.Contains(script, "set -x") {
			t.Errorf("%s: must not enable xtrace", name)
		}
		if strings.Contains(script, "insecure_bootstrap") {
			t.Errorf("%s: must not use insecure_bootstrap", name)
		}
		if strings.Contains(script, "[[ .") {
			t.Errorf("%s: unrendered template marker remains", name)
		}
	}
}

func TestRenderSpireScript(t *testing.T) {
	script, err := RenderSpireScript(testConfig(), testInfra())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		`readonly BUNDLE_PARAMETER="/crane/crane-platform/spire-bundle"`,
		`readonly TRUST_DOMAIN="crane.internal"`,
		`SPIRE_IMAGE="ghcr.io/spiffe/spire-server:1.9.3"`,
		`secretsmanager get-secret-value`,
		`sslmode=verify-full`,
		`sslrootcert=/etc/crane/rds-ca.pem`,
		`disable_instance_profile_selectors = true`,
		`CREATE DATABASE spire`,
		`CREATE DATABASE crane`,
		`--restart always`,
		`bundle show`,
		`aws ssm put-parameter`,
		`healthcheck`,
	}
	for _, marker := range want {
		if !strings.Contains(script, marker) {
			t.Errorf("spire script missing %q", marker)
		}
	}
	// The account must not be required as a validation allowlist anymore.
	if strings.Contains(script, "account_ids_for_local_validation") {
		t.Error("spire script must not rely on account_ids_for_local_validation")
	}
}

func TestRenderIdentityScript(t *testing.T) {
	script, err := RenderIdentityScript(testConfig(), testInfra(), "i-0abcdef1234567890", "i-11111111111111111")
	if err != nil {
		t.Fatal(err)
	}
	parent := "spiffe://crane.internal/spire/agent/aws_iid/123456789012/us-west-2/i-0abcdef1234567890"
	if !strings.Contains(script, parent) {
		t.Errorf("identity script missing deterministic parent ID %q", parent)
	}
	want := []string{
		`spiffe://crane.internal/dominator`,
		`spiffe://crane.internal/subd`,
		`k8s:ns:crane-system k8s:sa:dominator`,
		`k8s:ns:crane-system k8s:sa:crane-api`,
		`ensure_entry "spiffe://crane.internal/subd" "$PARENT_ID" unix:uid:10003`,
		`entry show`,
		`entry create`,
		`-parentID "$parent"`,
		`systemd:id:subd.service`,
		`spiffe://crane.internal/subd/i-11111111111111111`,
		`spiffe://crane.internal/subd/i-0abcdef1234567890`,
		`-matchSelectorsOn exact`,
	}
	for _, marker := range want {
		if !strings.Contains(script, marker) {
			t.Errorf("identity script missing %q", marker)
		}
	}
}

func TestRenderPlatformScript(t *testing.T) {
	script, err := RenderPlatformScript(testConfig(), testInfra(), testInfra().SpireAddress)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		`spire-agent`,
		`systemctl restart spire-agent.service`,
		`server_address = "internal-crane-spire.elb.us-west-2.amazonaws.com"`,
		`kubeadm init`,
		`--kubernetes-version "$K8S_VERSION"`,
		`--pod-network-cidr "$POD_CIDR"`,
		`test "$(kubeadm version -o short)" = "$K8S_VERSION"`,
		`helm upgrade --install cilium cilium/cilium`,
		`--version "$CILIUM_VERSION"`,
		`crane-bootstrap" -config /etc/crane/first-zone.rendered.json -pending`,
		`name: crane-api`,
		`name: dominator`,
		`hostNetwork: true`,
		`runAsUser: 10001`,
		`runAsUser: 10002`,
		`image: ghcr.io/nabutabu/crane-oss:0.2.0`,
		`image: ghcr.io/nabutabu/crane-oss-dominator:0.2.0`,
		`SPIFFE_ENDPOINT_SOCKET`,
		`tcpSocket:`,
		`installing the shared OTLP Collector`,
		`Collector queue requires at least 10 GiB free`,
		`CRANE_ZONE_ID`,
		`OTEL_EXPORTER_OTLP_ENDPOINT`,
		`otel-collector.crane-system.svc.cluster.local:4317`,
		`name: crane-otel-client`,
	}
	for _, marker := range want {
		if !strings.Contains(script, marker) {
			t.Errorf("platform script missing %q", marker)
		}
	}
	if strings.Contains(script, "set -x") {
		t.Error("platform script must not enable xtrace")
	}
}

func TestRenderCollectorManifest(t *testing.T) {
	data, err := baseScriptData(testConfig(), testInfra())
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := renderTemplate("otel-collector.yaml.tmpl", data)
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{
		`otlp_http/backend:`,
		`secretName: otel-exporter-config`,
		`--config=/etc/otel-exporter/exporter.yaml`,
		`name: crane-otel-receiver`,
		`crane.io/otel-config-hash: REPLACE_OTEL_CONFIG_HASH`,
		`path: /var/lib/crane/otel-queue`,
		`values: [REPLACE_PROTECTED_PLATFORM_NODE]`,
		`app: crane-api`,
		`app: dominator`,
	} {
		if !strings.Contains(manifest, marker) {
			t.Errorf("Collector manifest missing %q", marker)
		}
	}
	if strings.Contains(manifest, "[[ .") {
		t.Fatal("Collector manifest contains an unrendered template")
	}
}

func TestRenderValidateScript(t *testing.T) {
	script, err := RenderValidateScript(testConfig(), testInfra(), testAgentHosts())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		`rollout status deployment/crane-api`,
		`rollout status deployment/dominator`,
		`OBSERVABILITY_STATUS=configuration_pending`,
		`rollout status daemonset/cilium`,
		`setpriv --reuid=10003 --regid=10003 --clear-groups`,
		`crane-bootstrap-probe`,
		`UPDATE zones SET lifecycle_state = 'ready'`,
		`UPDATE clusters SET lifecycle_state = 'ready'`,
		`BEGIN;`,
		`COMMIT;`,
		`/etc/crane/first-zone.rendered.json`,
	}
	for _, marker := range want {
		if !strings.Contains(script, marker) {
			t.Errorf("validate script missing %q", marker)
		}
	}
}

func TestRenderScriptRequiredInputs(t *testing.T) {
	cfg, infra := testConfig(), testInfra()

	if _, err := RenderSpireScript(cfg, Infrastructure{}); err == nil {
		t.Error("spire script must reject missing infrastructure")
	}
	if _, err := RenderIdentityScript(cfg, infra, "  ", "i-11111111111111111"); err == nil {
		t.Error("identity script must reject empty instance ID")
	}
	if _, err := RenderPlatformScript(cfg, infra, ""); err == nil {
		t.Error("platform script must reject empty spire IP")
	}
	if _, err := RenderValidateScript(cfg, Infrastructure{}, testAgentHosts()); err == nil {
		t.Error("validate script must reject missing infrastructure")
	}
	noDomain := cfg
	noDomain.SPIRE.TrustDomain = ""
	if script, err := RenderSpireScript(noDomain, infra); err != nil || !strings.Contains(script, `trust_domain = "crane.internal"`) {
		t.Error("scripts must use the default trust domain when omitted")
	}
}

func TestRenderedScriptsAreValidBash(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	for name, script := range renderAll(t) {
		path := filepath.Join(t.TempDir(), name+".sh")
		if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command(bash, "-n", path).CombinedOutput()
		if err != nil {
			t.Errorf("%s: bash -n failed: %v\n%s", name, err, out)
		}
	}
}

func TestValidationDoesNotOpenAdmissionOnHealthFailure(t *testing.T) {
	for _, failure := range []string{"cluster", "identity"} {
		t.Run(failure, func(t *testing.T) {
			script, err := RenderValidateScript(testConfig(), testInfra(), testAgentHosts())
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			script = strings.ReplaceAll(script, "/opt/crane", dir)
			script = strings.ReplaceAll(script, "/var/lib/crane", dir)
			file := filepath.Join(dir, "validate.sh")
			if err = os.WriteFile(file, []byte(script), 0600); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(dir, "admission-opened")
			// Shell functions replace external services; this executes the real
			// ordering and exit-on-failure behavior without AWS, Kubernetes or UID changes.
			harness := `aws(){ printf '%s\n' '{"crane_username":"crane","crane_password":"test"}'; }
kubectl(){ ` + map[string]string{"cluster": "return 1;", "identity": "return 0;"}[failure] + ` }
setpriv(){ return 1; }
psql(){ touch ` + shellQuote(marker) + `; }
source ` + shellQuote(file)
			if output, err := exec.Command("bash", "-c", harness).CombinedOutput(); err == nil {
				t.Fatalf("health failure reported success: %s", output)
			}
			if _, err = os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("failed health gate reached admission update")
			}
		})
	}
}
