package awscompute

import (
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
}

func TestPersistedConfigNullFirstZoneUsesDefaultTopology(t *testing.T) {
	cfg := testConfig()
	cfg.FirstZone = []byte("null")
	cfg = cfg.WithDefaults()
	if len(cfg.FirstZone) != 0 {
		t.Fatal("null first_zone must normalize to omitted")
	}
	if _, err := topology(cfg, testInfra()); err != nil {
		t.Fatal(err)
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
