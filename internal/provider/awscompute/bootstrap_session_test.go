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
			want := "i-spire"
			if phase == bootstrap.InstallPlatform || phase == bootstrap.ValidateHandoff {
				want = "i-platform"
			}
			sent := 0
			session := &bootstrapSession{cfg: testConfig().WithDefaults(), infra: testInfra(), spire: host{ID: "i-spire"}, platform: host{ID: "i-platform"}}
			session.client = &fakeAWS{call: func(service, op string, input any) (any, error) {
				if service != "ssm" {
					t.Fatalf("unexpected service %s", service)
				}
				switch op {
				case "send-command":
					sent++
					if input.(map[string]any)["InstanceIds"].([]string)[0] != want {
						t.Fatal("wrong phase target")
					}
					return map[string]any{"Command": map[string]string{"CommandId": "command-test"}}, nil
				case "get-command-invocation":
					return map[string]string{"Status": "Success"}, nil
				default:
					t.Fatalf("unexpected operation %s", op)
					return nil, nil
				}
			}}
			if err := session.RunHostPhase(context.Background(), phase, bootstrap.NewReporter(io.Discard, false)); err != nil {
				t.Fatal(err)
			}
			if sent != 1 {
				t.Fatalf("sent %d commands", sent)
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
