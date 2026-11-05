package awscompute

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestEndpointRejectsIncompatibleOrUnownedResources(t *testing.T) {
	for _, tc := range []string{"public", "wrong-vpc", "wrong-subnets", "wrong-security-group", "unowned", "bad-target", "tls-listener", "denied"} {
		t.Run(tc, func(t *testing.T) {
			cfg, infra := testConfig(), testInfra()
			lb := map[string]any{"LoadBalancerArn": "arn:lb", "DNSName": infra.SpireAddress, "Type": "network", "Scheme": "internal", "VpcId": infra.VPCID, "SecurityGroups": []string{infra.SpireLoadBalancerSecurityGroupID}, "AvailabilityZones": []any{map[string]string{"SubnetId": infra.PrivateSubnetIDs[0]}, map[string]string{"SubnetId": infra.PrivateSubnetIDs[1]}}}
			switch tc {
			case "public":
				lb["Scheme"] = "internet-facing"
			case "wrong-vpc":
				lb["VpcId"] = "vpc-other"
			case "wrong-subnets":
				lb["AvailabilityZones"] = []any{}
			case "wrong-security-group":
				lb["SecurityGroups"] = []string{"sg-other"}
			}
			aws := &fakeAWS{call: func(service, op string, in any) (any, error) {
				switch op {
				case "describe-load-balancers":
					if tc == "denied" {
						return nil, &AWSError{Code: "AccessDenied"}
					}
					return map[string]any{"LoadBalancers": []any{lb}}, nil
				case "describe-tags":
					tags := resourceTags(cfg.Name, "endpoint")
					if tc == "unowned" {
						tags = nil
					}
					return map[string]any{"TagDescriptions": []any{map[string]any{"Tags": tags}}}, nil
				case "modify-load-balancer-attributes", "modify-target-group-attributes":
					return nil, nil
				case "describe-target-groups":
					protocol := "TCP"
					if tc == "bad-target" {
						protocol = "TLS"
					}
					return map[string]any{"TargetGroups": []any{map[string]any{"TargetGroupArn": "arn:tg", "VpcId": infra.VPCID, "Protocol": protocol, "Port": 8081, "TargetType": "instance", "HealthCheckProtocol": "TCP", "HealthCheckPort": "8081"}}}, nil
				case "describe-listeners":
					return map[string]any{"Listeners": []any{map[string]any{"Protocol": "TLS", "Port": 8081}}}, nil
				default:
					t.Fatalf("unexpected endpoint mutation %s:%s", service, op)
					return nil, nil
				}
			}}
			if _, err := ensureSpireEndpoint(context.Background(), aws, cfg, infra); err == nil {
				t.Fatal("accepted incompatible endpoint")
			}
		})
	}
}

func TestSpireTargetRegistrationNeverMixesAuthorities(t *testing.T) {
	for _, existing := range []string{"", "i-spire", "i-other"} {
		t.Run(existing, func(t *testing.T) {
			registered := false
			aws := &fakeAWS{call: func(_, op string, in any) (any, error) {
				switch op {
				case "describe-target-health":
					targets := []any{}
					if existing != "" {
						targets = append(targets, map[string]any{"Target": map[string]any{"Id": existing, "Port": 8081}})
					}
					return map[string]any{"TargetHealthDescriptions": targets}, nil
				case "register-targets":
					registered = true
					return nil, nil
				default:
					return nil, fmt.Errorf("unexpected operation %s", op)
				}
			}}
			err := registerSpireTarget(context.Background(), aws, testInfra(), "i-spire")
			if existing == "i-other" {
				if err == nil || registered {
					t.Fatal("registered a second authority")
				}
			} else if err != nil || !registered {
				t.Fatalf("cannot register/retry target: %v", err)
			}
		})
	}
}

func TestGoldenImageMustMatchEndpointAndIdentity(t *testing.T) {
	cfg, infra := testConfig(), testInfra()
	base := map[string]string{"CraneImage": "v2", "AWSRegion": cfg.AWS.Region, "SpireServerAddress": infra.SpireAddress, "SpireTrustDomain": cfg.SPIRE.TrustDomain, "SpireBundleParameter": bundleParameter(cfg), "SpireVersion": cfg.SPIRE.Version, "KubernetesVersion": cfg.Platform.KubernetesVersion}
	for _, bad := range []string{"", "SpireServerAddress", "SpireTrustDomain", "SpireBundleParameter", "SpireVersion", "KubernetesVersion", "AWSRegion", "CraneImage", "architecture"} {
		t.Run(bad, func(t *testing.T) {
			tags := []tag{}
			for k, v := range base {
				if k == bad {
					v = "wrong"
				}
				tags = append(tags, tag{k, v})
			}
			arch := "x86_64"
			if bad == "architecture" {
				arch = "arm64"
			}
			aws := &fakeAWS{call: func(_, op string, _ any) (any, error) {
				return map[string]any{"Images": []any{map[string]any{"State": "available", "Architecture": arch, "Tags": tags}}}, nil
			}}
			err := validateGoldenImage(context.Background(), aws, cfg, infra, "ami-golden")
			if (err != nil) != (bad != "") {
				t.Fatalf("unexpected image validation: %v", err)
			}
		})
	}
}

func TestAttachedZoneRecoversSpireEndpointFromPinnedImage(t *testing.T) {
	cfg, infra := testConfig(), testInfra()
	tags := []tag{
		{"CraneImage", "v2"},
		{"AWSRegion", cfg.AWS.Region},
		{"SpireServerAddress", infra.SpireAddress},
		{"SpireTrustDomain", cfg.SPIRE.TrustDomain},
		{"SpireBundleParameter", bundleParameter(cfg)},
		{"SpireVersion", cfg.SPIRE.Version},
		{"KubernetesVersion", cfg.Platform.KubernetesVersion},
	}
	for _, tc := range []struct {
		name, key, value string
		wantError        bool
	}{
		{name: "valid"},
		{name: "missing endpoint", key: "SpireServerAddress", value: "", wantError: true},
		{name: "other deployment bundle", key: "SpireBundleParameter", value: "/crane/other/spire-bundle", wantError: true},
		{name: "other trust domain", key: "SpireTrustDomain", value: "other.internal", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			imageTags := append([]tag(nil), tags...)
			for i := range imageTags {
				if imageTags[i].Key == tc.key {
					imageTags[i].Value = tc.value
				}
			}
			aws := &fakeAWS{call: func(service, operation string, input any) (any, error) {
				if service != "ec2" || operation != "describe-images" {
					t.Fatalf("unexpected AWS call %s:%s", service, operation)
				}
				return map[string]any{"Images": []any{map[string]any{"State": "available", "Architecture": "x86_64", "Tags": imageTags}}}, nil
			}}
			address, err := goldenImageSpireAddress(context.Background(), aws, cfg, "ami-pinned", "")
			if tc.wantError {
				if err == nil {
					t.Fatal("accepted incompatible image metadata")
				}
				return
			}
			if err != nil || address != infra.SpireAddress {
				t.Fatalf("address %q, error %v", address, err)
			}
			script, err := renderTemplate("subd.sh.tmpl", scriptData{SpireAddress: address})
			if err != nil || !strings.Contains(script, `server_address = "`+address+`"`) {
				t.Fatalf("Subd script lost pinned SPIRE address: %v", err)
			}
		})
	}
}

func TestEndpointNamesAreBoundedAndDistinct(t *testing.T) {
	a, b := endpointName(strings.Repeat("a", 29)+"b"), endpointName(strings.Repeat("a", 29)+"c")
	if a == b || len(a) > 32 || len(b) > 32 {
		t.Fatal("ELB names collide or exceed AWS limits")
	}
}
