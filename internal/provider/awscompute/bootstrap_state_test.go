package awscompute

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestBootstrapResolvePinsPlacementAndImages(t *testing.T) {
	cfg := testConfig().WithDefaults()
	cfg.SPIRE.AMIID = ""
	cfg.Platform.AMIID = ""
	client := &fakeAWS{call: func(service, op string, _ any) (any, error) {
		switch service + ":" + op {
		case "sts:get-caller-identity":
			return map[string]string{"Account": cfg.AWS.AccountID}, nil
		case "ec2:describe-availability-zones":
			return map[string]any{"AvailabilityZones": []any{map[string]string{"ZoneName": "us-west-2b", "State": "available"}, map[string]string{"ZoneName": "us-west-2a", "State": "available"}}}, nil
		default:
			t.Fatalf("unexpected operation %s:%s", service, op)
			return nil, nil
		}
	}}
	session := &bootstrapSession{cfg: cfg, client: client}
	pinned, err := session.Resolve(context.Background(), nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if session.cfg.Network.AvailabilityZones[0] != "us-west-2a" {
		t.Fatal("placement not deterministic")
	}
	candidate := cfg
	candidate.SPIRE.AMIID = "ami-golden"
	candidate.Platform.AMIID = "ami-golden"
	next := &bootstrapSession{cfg: candidate, client: client}
	pinned, err = next.Resolve(context.Background(), pinned, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(client.calls) != 3 {
		t.Fatalf("retry rediscovered placement: %v", client.calls)
	}
	for _, change := range []string{"ami", "account", "region", "network", "placement"} {
		t.Run(change, func(t *testing.T) {
			candidate := next.cfg
			// Copy through JSON so edits never mutate slices held by the prior session.
			data, _ := json.Marshal(candidate)
			json.Unmarshal(data, &candidate)
			switch change {
			case "ami":
				candidate.Platform.AMIID = "ami-other"
			case "account":
				candidate.AWS.AccountID = "999999999999"
			case "region":
				candidate.AWS.Region = "us-east-1"
			case "network":
				candidate.Network.VPCCIDR = "10.90.0.0/16"
			case "placement":
				candidate.Network.AvailabilityZones[0] = "us-west-2c"
			}
			denied := &fakeAWS{call: func(_, op string, _ any) (any, error) { t.Fatalf("call after changed config: %s", op); return nil, nil }}
			retry := &bootstrapSession{cfg: candidate, client: denied}
			if _, err := retry.Resolve(context.Background(), pinned, false); err == nil {
				t.Fatal("accepted changed deployment")
			}
		})
	}
}

func TestBootstrapAccountMismatchAndMissingImages(t *testing.T) {
	for _, missing := range []bool{true, false} {
		session := &bootstrapSession{cfg: testConfig().WithDefaults(), client: &fakeAWS{call: func(_, op string, _ any) (any, error) {
			if missing {
				t.Fatalf("provider call with missing images: %s", op)
			}
			return map[string]string{"Account": "999999999999"}, nil
		}}}
		if !missing {
			session.cfg.SPIRE.AMIID = "ami-pinned"
			session.cfg.Platform.AMIID = "ami-pinned"
		}
		if _, err := session.Resolve(context.Background(), nil, false); err == nil {
			t.Fatal("expected validation failure")
		}
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
