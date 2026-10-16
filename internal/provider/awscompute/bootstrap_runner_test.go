package awscompute

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type fakeAWS struct {
	call  func(string, string, any) (any, error)
	calls []string
}

func (f *fakeAWS) Call(_ context.Context, service, operation string, input, output any) error {
	f.calls = append(f.calls, service+":"+operation)
	value, err := f.call(service, operation, input)
	if err != nil {
		return err
	}
	if output != nil {
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		return json.Unmarshal(data, output)
	}
	return nil
}

func TestHostRecoveryReusesInstance(t *testing.T) {
	aws := &fakeAWS{call: func(service, op string, input any) (any, error) {
		switch service + ":" + op {
		case "ec2:describe-instances":
			return map[string]any{"Reservations": []any{map[string]any{"Instances": []any{map[string]any{"InstanceId": "i-existing", "ImageId": "ami-pinned", "InstanceType": "t3.small", "PrivateIpAddress": "10.80.0.10", "State": map[string]string{"Name": "running"}}}}}}, nil
		case "ssm:describe-instance-information":
			return map[string]any{"InstanceInformationList": []any{map[string]string{"InstanceId": "i-existing", "PingStatus": "Online"}}}, nil
		default:
			t.Fatalf("unexpected call %s:%s", service, op)
			return nil, nil
		}
	}}
	got, err := ensureHost(context.Background(), aws, bootstrapConfig{Name: "test"}, Infrastructure{}, "spire", "ami-pinned", "t3.small", 20, "profile", "sg-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "i-existing" || got.PrivateIP != "10.80.0.10" {
		t.Fatalf("unexpected recovered host %+v", got)
	}
}

func TestHostDescribeDeniedNeverCreates(t *testing.T) {
	denied := &AWSError{Code: "AccessDenied"}
	aws := &fakeAWS{call: func(_, op string, _ any) (any, error) {
		if op != "describe-instances" {
			t.Fatalf("mutation after denied discovery: %s", op)
		}
		return nil, denied
	}}
	_, err := ensureHost(context.Background(), aws, bootstrapConfig{Name: "test"}, Infrastructure{}, "spire", "ami", "t3.small", 20, "profile", "sg")
	if !errors.Is(err, denied) {
		t.Fatal(err)
	}
}

func TestHostLaunchUsesStableTokenAndMetadataProtection(t *testing.T) {
	var requests []map[string]any
	aws := &fakeAWS{call: func(service, op string, input any) (any, error) {
		switch service + ":" + op {
		case "ec2:describe-instances":
			return map[string]any{"Reservations": []any{}}, nil
		case "ec2:run-instances":
			requests = append(requests, input.(map[string]any))
			return map[string]any{"Instances": []any{map[string]string{"InstanceId": "i-new", "PrivateIpAddress": "10.80.0.10"}}}, nil
		case "ssm:describe-instance-information":
			return map[string]any{"InstanceInformationList": []any{map[string]string{"InstanceId": "i-new", "PingStatus": "Online"}}}, nil
		}
		t.Fatalf("unexpected op %s:%s", service, op)
		return nil, nil
	}}
	cfg := bootstrapConfig{Name: "test", AWS: AWSConfig{Region: "us-west-2", AccountID: "123456789012"}}
	infra := Infrastructure{PublicSubnetIDs: []string{"subnet-1"}}
	for range 2 {
		if _, err := ensureHost(context.Background(), aws, cfg, infra, "spire", "ami-pinned", "t3.small", 20, "profile", "sg"); err != nil {
			t.Fatal(err)
		}
	}
	if requests[0]["ClientToken"] != requests[1]["ClientToken"] {
		t.Fatal("launch retry changed ClientToken")
	}
	metadata := requests[0]["MetadataOptions"].(map[string]any)
	if metadata["HttpTokens"] != "required" || metadata["HttpPutResponseHopLimit"] != 2 {
		t.Fatal("missing metadata protection")
	}
	if _, exists := requests[0]["KeyName"]; exists {
		t.Fatal("bootstrap must not require an SSH key")
	}
}

func TestPauseHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !errors.Is(pause(ctx, time.Hour), context.Canceled) {
		t.Fatal("wait ignored cancellation")
	}
}
