package awscompute

import (
	"encoding/json"
	"testing"

	"github.com/nabutabu/crane-oss/internal/capacity"
	"github.com/nabutabu/crane-oss/pkg/api"
)

func TestDecodeAttachmentConfigRejectsInvalidPlacementAndCIDRs(t *testing.T) {
	deployment := capacity.DeploymentRuntime{Provider: "aws", AccountID: "123", Region: "us-west-2", VPCID: "vpc-owned", PodCIDR: "10.244.0.0/16"}
	zone := api.ZoneInput{Provider: "aws", AccountID: "123", Region: "us-west-2", ProviderZoneID: "us-west-2b"}
	for _, tc := range []struct {
		name, raw string
		valid     bool
	}{
		{"disjoint", `{"public_subnet_cidr":"10.0.4.0/24","private_subnet_cidr":"10.0.5.0/24"}`, true},
		{"overlap", `{"public_subnet_cidr":"10.0.4.0/24","private_subnet_cidr":"10.0.4.128/25"}`, false},
		{"pod overlap", `{"public_subnet_cidr":"10.244.1.0/24","private_subnet_cidr":"10.0.5.0/24"}`, false},
		{"unknown", `{"public_subnet_cidr":"10.0.4.0/24","private_subnet_cidr":"10.0.5.0/24","vpc_id":"vpc-other"}`, false},
		{"missing", `{}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeAttachmentConfig(json.RawMessage(tc.raw), deployment, zone)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
	zone.AccountID = "other"
	if _, err := DecodeAttachmentConfig(json.RawMessage(`{"public_subnet_cidr":"10.0.4.0/24","private_subnet_cidr":"10.0.5.0/24"}`), deployment, zone); err == nil {
		t.Fatal("foreign account accepted")
	}
}
