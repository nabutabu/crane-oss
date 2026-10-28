package bootstrap

import (
	"encoding/json"
	"testing"
)

func TestConfigRejectsOldVersionsAndInvalidSharedInputs(t *testing.T) {
	for _, edit := range []func(*Config){
		func(c *Config) { c.DevelopmentStage = "" },
		func(c *Config) { c.GroupSuffix = "" },
		func(c *Config) { c.Version = 2 },
		func(c *Config) { c.Provider = "" },
		func(c *Config) { c.ProviderConfig = json.RawMessage(`null`) },
		func(c *Config) { c.Platform.CraneAPIImage = "registry.example/control:latest" },
		func(c *Config) { c.SPIRE.TrustDomain = "invalid;command" },
		func(c *Config) { c.FirstZone = json.RawMessage(`{}`) },
	} {
		cfg := runConfig()
		edit(&cfg)
		if err := cfg.Validate(); err == nil {
			t.Fatal("accepted invalid config")
		}
	}
}

func TestTopologyRequiresConsistentProviderPlacement(t *testing.T) {
	in, err := FirstZone(runConfig())
	if err != nil {
		t.Fatal(err)
	}
	in.Zone.Provider, in.Zone.AccountID, in.Zone.Region, in.Zone.ProviderZoneID = "unit", "account", "region", "zone"
	in.Group.ProviderZoneID = "zone"
	in.Group.Naming.Region, in.Group.Naming.AZ = "region", "a"
	if err := in.Group.NormalizeID(); err != nil {
		t.Fatal(err)
	}
	in.Group.LaunchParameters = json.RawMessage(`{"image":"pinned"}`)
	if err := ValidateTopology(in); err != nil {
		t.Fatal(err)
	}
	in.Group.ProviderZoneID = "another-zone"
	if err := ValidateTopology(in); err == nil {
		t.Fatal("accepted cross-zone topology")
	}
}
