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

func TestValidateObservabilityConfig(t *testing.T) {
	cfg := testConfig()
	cfg.Observability.NewRelicOTLPEndpoint = ""
	if got := cfg.WithDefaults().Observability.NewRelicOTLPEndpoint; got != "https://otlp.nr-data.net" {
		t.Fatalf("unexpected default endpoint %q", got)
	}
	for _, tc := range []struct {
		name string
		edit func(*bootstrapConfig)
	}{
		{"wrong account", func(c *bootstrapConfig) {
			c.Observability.NewRelicSecretARN = "arn:aws:secretsmanager:us-west-2:999999999999:secret:crane/new-relic-abcd"
		}},
		{"wrong region", func(c *bootstrapConfig) {
			c.Observability.NewRelicSecretARN = "arn:aws:secretsmanager:us-east-1:123456789012:secret:crane/new-relic-abcd"
		}},
		{"http endpoint", func(c *bootstrapConfig) { c.Observability.NewRelicOTLPEndpoint = "http://otlp.nr-data.net" }},
		{"endpoint credentials", func(c *bootstrapConfig) { c.Observability.NewRelicOTLPEndpoint = "https://user:pass@otlp.nr-data.net" }},
		{"endpoint path", func(c *bootstrapConfig) { c.Observability.NewRelicOTLPEndpoint = "https://otlp.nr-data.net/v1/logs" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := cfg
			tc.edit(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("expected invalid observability configuration")
			}
		})
	}
}
