package awscompute

import (
	"context"
	"io"
	"testing"

	"github.com/nabutabu/crane-oss/internal/bootstrap"
)

func TestBootstrapHostPhasesUseTheCorrectProtectedHost(t *testing.T) {
	for _, phase := range []bootstrap.HostPhase{bootstrap.InstallIdentity, bootstrap.RegisterWorkloads, bootstrap.InstallPlatform, bootstrap.ValidateHandoff} {
		t.Run(string(phase), func(t *testing.T) {
			want := "i-11111111111111111"
			if phase == bootstrap.InstallPlatform || phase == bootstrap.ValidateHandoff {
				want = "i-0abcdef1234567890"
			}
			sent := 0
			session := &bootstrapSession{cfg: testConfig().WithDefaults(), infra: testInfra(), spire: host{ID: "i-11111111111111111", PrivateIP: "10.80.0.11"}, platform: host{ID: "i-0abcdef1234567890", PrivateIP: "10.80.0.12"}}
			session.client = &fakeAWS{call: func(service, op string, input any) (any, error) {
				if service != "ssm" {
					t.Fatalf("unexpected service %s", service)
				}
				switch op {
				case "send-command":
					sent++
					target := want
					if phase == bootstrap.ValidateHandoff && sent == 1 {
						target = session.spire.ID
					}
					if input.(map[string]any)["InstanceIds"].([]string)[0] != target {
						t.Fatal("wrong phase target")
					}
					return map[string]any{"Command": map[string]string{"CommandId": "command-test"}}, nil
				case "get-command-invocation":
					return map[string]string{"Status": "Success", "StandardOutputContent": "crane-observability-status:ready"}, nil
				default:
					t.Fatalf("unexpected operation %s", op)
					return nil, nil
				}
			}}
			if err := session.RunHostPhase(context.Background(), phase, bootstrap.NewReporter(io.Discard, false)); err != nil {
				t.Fatal(err)
			}
			wantSent := 1
			if phase == bootstrap.ValidateHandoff {
				wantSent = 4
			}
			if sent != wantSent {
				t.Fatalf("sent %d commands", sent)
			}
			if phase == bootstrap.ValidateHandoff {
				result := session.Result()
				if result.ObservabilityStatus != "ready" || result.TargetHostID != "crane-platform/bootstrap/platform" || result.ControlPlaneEndpoint != "http://10.80.0.12:43060" || result.ConnectionInstructions == "" {
					t.Fatal("invalid API handoff result")
				}
			}
		})
	}
}

func TestBootstrapRejectsUnknownHostPhase(t *testing.T) {
	session := &bootstrapSession{}
	if err := session.RunHostPhase(context.Background(), "unknown", bootstrap.NewReporter(io.Discard, false)); err == nil {
		t.Fatal("accepted unknown phase")
	}
}
