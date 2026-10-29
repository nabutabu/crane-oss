package awscompute

import (
	"context"
	"encoding/json"
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
	if err := s.EnsureInfrastructure(context.Background(), api.Operation{Kind: "attach_zone", ZoneID: zone.ID, Parameters: parameters}); err == nil {
		t.Fatal("missing deployment VPC accepted")
	}
	if len(client.calls) != 2 {
		t.Fatalf("unexpected calls %v", client.calls)
	}
}
