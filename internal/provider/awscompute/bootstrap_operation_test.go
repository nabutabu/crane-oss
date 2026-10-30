package awscompute

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nabutabu/crane-oss/pkg/api"
)

func TestOperationTargetBindsToPinnedDeployment(t *testing.T) {
	s := &operationSession{cfg: bootstrapConfig{Name: "deployment", AWS: AWSConfig{Region: "us-west-2", AccountID: "account"}}}
	zone := api.ZoneInput{ID: "zone-b", Provider: "aws", AccountID: "account", Region: "us-west-2", ProviderZoneID: "us-west-2b"}
	parameters, _ := json.Marshal(map[string]any{"zone": zone, "membership": api.ClusterZoneInput{ClusterID: "cluster", ZoneID: zone.ID}})
	op := api.Operation{Kind: "attach_zone", ZoneID: zone.ID, Parameters: parameters}
	if _, _, err := s.target(op); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*api.ZoneInput)
	}{
		{"provider", func(z *api.ZoneInput) { z.Provider = "other" }},
		{"account", func(z *api.ZoneInput) { z.AccountID = "other" }},
		{"region", func(z *api.ZoneInput) { z.Region = "other" }},
		{"zone", func(z *api.ZoneInput) { z.ID = "other" }},
	} {
		z := zone
		tc.change(&z)
		p, _ := json.Marshal(map[string]any{"zone": z, "membership": api.ClusterZoneInput{ClusterID: "cluster", ZoneID: z.ID}})
		op.Parameters = p
		if _, _, err := s.target(op); err == nil {
			t.Fatalf("accepted %s mismatch", tc.name)
		}
	}
}

func TestAdditionalClusterControlPlaneScript(t *testing.T) {
	script, err := renderTemplate("cluster-control-plane.sh.tmpl", scriptData{KubernetesVersion: "v1.35.0", PodCIDR: "10.244.0.0/16", Host: bootstrapAgentHost{PrivateIP: "10.80.1.20"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"kubeadm init", "/etc/kubernetes/admin.conf", "crane-cluster-api-endpoint:", "crane-cluster-ca-hash:"} {
		if !strings.Contains(script, required) {
			t.Fatalf("missing %s", required)
		}
	}
	for _, forbidden := range []string{"spire-server", "crane-bootstrap -pending", "CREATE DATABASE"} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("unexpected %s", forbidden)
		}
	}
}

func TestZoneDominatorScriptUsesZoneIdentityAndRuntimeSecret(t *testing.T) {
	script, err := renderTemplate("zone-dominator.sh.tmpl", scriptData{
		Config:      bootstrapConfig{RDS: RDSConfig{Port: 5432}, Platform: PlatformConfig{DominatorImage: "ghcr.io/nabutabu/dominator:1.2.3"}},
		Infra:       Infrastructure{RuntimeSecretARN: "arn:aws:secretsmanager:us-west-2:123456789012:secret:runtime", DBEndpoint: "db.internal"},
		Region:      "us-west-2",
		TrustDomain: "crane.internal",
		ZoneID:      "zone-b",
		Host:        bootstrapAgentHost{PrivateIP: "10.80.1.20"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"dominator-zone-b",
		"serviceAccountName: dominator-zone-b",
		"arn:aws:secretsmanager:us-west-2:123456789012:secret:runtime",
		"DB_HOST",
		"name: dominator-zone-b",
		"port: 44000",
		"crane-dominator-endpoint:",
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("missing %s", required)
		}
	}
	if strings.Contains(script, "hostNetwork: true") {
		t.Fatal("zone Dominator must use its private Service rather than the host port")
	}
}

func TestInitializeControlPlaneReturnsPublicDiscovery(t *testing.T) {
	s := &operationSession{cfg: bootstrapConfig{Name: "deployment", AWS: AWSConfig{Region: "us-west-2", AccountID: "account"}, Platform: PlatformConfig{AMIID: "ami-pinned", InstanceType: "t3.medium", KubernetesVersion: "v1.35.0", PodCIDR: "10.244.0.0/16"}}}
	s.client = &fakeAWS{call: func(service, operation string, input any) (any, error) {
		switch service + ":" + operation {
		case "ec2:describe-instances":
			return map[string]any{"Reservations": []any{map[string]any{"Instances": []any{map[string]any{"InstanceId": "i-0123456789abcdef0", "PrivateIPAddress": "10.80.1.20", "ImageId": "ami-pinned", "InstanceType": "t3.medium", "State": map[string]string{"Name": "running"}}}}}}, nil
		case "ssm:send-command":
			return map[string]any{"Command": map[string]string{"CommandId": "command-id"}}, nil
		case "ssm:get-command-invocation":
			return map[string]string{"Status": "Success", "StandardOutputContent": "crane-cluster-api-endpoint:https://10.80.1.20:6443\ncrane-cluster-ca-hash:sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n"}, nil
		default:
			t.Fatalf("unexpected AWS call %s:%s", service, operation)
			return nil, nil
		}
	}}
	zone := api.ZoneInput{ID: "zone-b", Provider: "aws", AccountID: "account", Region: "us-west-2", ProviderZoneID: "us-west-2b"}
	params, _ := json.Marshal(map[string]any{"first_zone": api.FirstZoneInput{Zone: zone, Cluster: api.ClusterInput{ID: "cluster-b"}}})
	result, err := s.InitializeControlPlane(context.Background(), api.Operation{Kind: "create_cluster", ZoneID: zone.ID, Parameters: params})
	if err != nil {
		t.Fatal(err)
	}
	if result.APIServerEndpoint != "https://10.80.1.20:6443" || !strings.HasPrefix(result.CACertHash, "sha256:") {
		t.Fatalf("discovery %+v", result)
	}
}

func TestOperationDoesNotCreateAnotherDeploymentVPC(t *testing.T) {
	s := &operationSession{cfg: bootstrapConfig{Name: "deployment", AWS: AWSConfig{Region: "us-west-2", AccountID: "account"}}}
	client := &fakeAWS{call: func(service, operation string, input any) (any, error) {
		switch service + ":" + operation {
		case "sts:get-caller-identity":
			return map[string]string{"Account": "account"}, nil
		case "ec2:describe-vpcs":
			return map[string]any{"Vpcs": []any{}}, nil
		default:
			t.Fatalf("unexpected AWS call %s:%s", service, operation)
			return nil, nil
		}
	}}
	s.client = client
	zone := api.ZoneInput{ID: "zone-b", Provider: "aws", AccountID: "account", Region: "us-west-2", ProviderZoneID: "us-west-2b"}
	parameters, _ := json.Marshal(map[string]any{"zone": zone, "membership": api.ClusterZoneInput{ClusterID: "cluster", ZoneID: zone.ID}})
	if _, err := s.EnsureInfrastructure(context.Background(), api.Operation{Kind: "attach_zone", ZoneID: zone.ID, Parameters: parameters}); err == nil {
		t.Fatal("missing deployment VPC accepted")
	}
	if len(client.calls) != 2 {
		t.Fatalf("unexpected calls %v", client.calls)
	}
}
