package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalStatePinsImagesAndRejectsChangedDeployment(t *testing.T) {
	cfg := testConfig().WithDefaults()
	cfg.Platform.CraneAPIImage = ""
	cfg.Platform.DominatorImage = ""
	dir := t.TempDir()
	aws := &fakeAWS{call: func(_, op string, _ any) (any, error) {
		if op != "describe-availability-zones" {
			t.Fatalf("unexpected AWS operation %s", op)
		}
		return map[string]any{"AvailabilityZones": []any{map[string]string{"ZoneName": "us-west-2b", "State": "available"}, map[string]string{"ZoneName": "us-west-2a", "State": "available"}}}, nil
	}}
	if err := pinDeployment(context.Background(), aws, &cfg, dir); err != nil {
		t.Fatal(err)
	}
	if cfg.Network.AvailabilityZones[0] != "us-west-2a" {
		t.Fatal("zones not pinned deterministically")
	}
	cfg.SPIRE.AMIID = "ami-golden"
	cfg.Platform.AMIID = "ami-golden"
	cfg.Platform.CraneAPIImage = "ghcr.io/nabutabu/crane-oss:0.2.0"
	cfg.Platform.DominatorImage = "ghcr.io/nabutabu/crane-oss-dominator:0.2.0"
	if err := pinDeployment(context.Background(), aws, &cfg, dir); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(aws.calls) != 1 {
		t.Fatal("retry rediscovered availability zones")
	}
	fi, err := os.Stat(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0600 {
		t.Fatalf("state mode %o", fi.Mode().Perm())
	}
	for _, change := range []string{"ami", "image", "account", "network"} {
		candidate := cfg
		switch change {
		case "ami":
			candidate.Platform.AMIID = "ami-other"
		case "image":
			candidate.Platform.CraneAPIImage = "ghcr.io/nabutabu/crane-oss:0.3.0"
		case "account":
			candidate.AWS.AccountID = "999999999999"
		case "network":
			candidate.Network.VPCCIDR = "10.90.0.0/16"
		}
		if err := pinDeployment(context.Background(), aws, &candidate, dir); err == nil {
			t.Fatalf("accepted changed %s", change)
		}
		after, _ := os.ReadFile(filepath.Join(dir, "config.json"))
		if string(before) != string(after) {
			t.Fatal("failed validation changed durable state")
		}
	}
}

func TestLocalStateRejectsCorruptionWithoutAWSWrites(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig()
	aws := &fakeAWS{call: func(_, op string, _ any) (any, error) {
		t.Fatalf("AWS call after corrupt local state: %s", op)
		return nil, nil
	}}
	if err := pinDeployment(context.Background(), aws, &cfg, dir); err == nil {
		t.Fatal("ignored corrupt state")
	}
}

func TestImageReferencesFollowReleaseWorkflow(t *testing.T) {
	for _, ref := range []string{"ghcr.io/nabutabu/crane-oss:0.2.0", "ghcr.io/nabutabu/crane-oss:" + strings.Repeat("a", 40), "123456789012.dkr.ecr.us-west-2.amazonaws.com/crane-oss@sha256:" + strings.Repeat("a", 64), "ghcr.io/nabutabu/crane-oss:latest", "ghcr.io/nabutabu/crane-oss"} {
		cfg := testConfig()
		cfg.Platform.CraneAPIImage = ref
		valid := strings.Contains(ref, ":0.2.0") || strings.Contains(ref, strings.Repeat("a", 40))
		if err := cfg.Validate(); (err == nil) != valid {
			t.Fatalf("image %s validation: %v", ref, err)
		}
	}
}
