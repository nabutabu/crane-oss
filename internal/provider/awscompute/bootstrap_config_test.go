package awscompute

import (
	"encoding/json"
	"testing"
)

func TestEmptyAccountConfig(t *testing.T) {
	cfg := bootstrapConfig{AWS: AWSConfig{Region: "us-west-2"}}.WithDefaults()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.Network.VPCID != "" || len(cfg.Network.PrivateSubnetIDs) != 0 || cfg.SPIRE.IAMInstanceProfile != "" {
		t.Fatal("empty account should not require existing resources")
	}
	if !cfg.RDS.ManageMasterUserPassword || !cfg.RDS.DeletionProtection {
		t.Fatal("RDS must protect storage and manage the master secret")
	}
	if cfg.Platform.StorageGiB != 40 {
		t.Fatalf("platform volume default must reserve room for the Collector queue, got %d GiB", cfg.Platform.StorageGiB)
	}
}

func TestPersistedConfigNullFirstZoneUsesDefaultTopology(t *testing.T) {
	cfg := testConfig()
	cfg.FirstZone = []byte("null")
	cfg = cfg.WithDefaults()
	if len(cfg.FirstZone) != 0 {
		t.Fatal("null first_zone must normalize to omitted")
	}
	in, err := topology(cfg, testInfra())
	if err != nil {
		t.Fatal(err)
	}
	if in.Group.Scheduling.InstanceType != cfg.Platform.InstanceType || in.Group.Scheduling.Zone != "us-west-2a" || in.Group.Scheduling.Region != cfg.AWS.Region || in.Group.Scheduling.Architecture != "amd64" {
		t.Fatalf("bootstrap group lacks local autoscaler template: %+v", in.Group.Scheduling)
	}
}

func TestRejectInvalidBootstrapNetwork(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*bootstrapConfig)
	}{
		{"overlap", func(c *bootstrapConfig) { c.Network.PrivateSubnetCIDRs[0] = c.Network.PublicSubnetCIDRs[0] }},
		{"outside", func(c *bootstrapConfig) { c.Network.PrivateSubnetCIDRs[0] = "192.168.1.0/24" }},
		{"pod overlap", func(c *bootstrapConfig) { c.Platform.PodCIDR = "10.80.0.0/16" }},
		{"manual network", func(c *bootstrapConfig) { c.Network.VPCID = "vpc-existing" }},
		{"floating release", func(c *bootstrapConfig) { c.Platform.CiliumVersion = "latest" }},
		{"shell injection", func(c *bootstrapConfig) { c.SPIRE.TrustDomain = "example;id" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := bootstrapConfig{AWS: AWSConfig{Region: "us-west-2"}}.WithDefaults()
			tc.edit(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("expected invalid configuration")
			}
		})
	}
}

func TestInputRejectsRetiredBackendConfiguration(t *testing.T) {
	for _, raw := range []string{
		`{"observability":{"new_relic_secret_arn":"retired-reference"}}`,
		`{"unexpected_field":true}`,
	} {
		if _, err := decodeBootstrapSettings(json.RawMessage(raw)); err == nil {
			t.Fatal("input accepted unsupported provider settings")
		}
	}
}
