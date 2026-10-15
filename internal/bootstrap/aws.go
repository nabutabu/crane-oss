package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
)

// AWSClient allows offline tests to exercise AWS requests without an account.
// Call receives the AWS CLI service/operation and the API's JSON input shape.
type AWSClient interface {
	Call(context.Context, string, string, any, any) error
}

// CLI uses the AWS CLI credential chain; no credentials are copied into config.
type CLI struct{ Region, Profile string }

// AWSError preserves only the AWS error code, never the service's message.
type AWSError struct {
	Service, Operation, Code string
	Err                      error
}

func (e *AWSError) Error() string {
	return fmt.Sprintf("AWS %s %s failed (%s)", e.Service, e.Operation, e.Code)
}
func (e *AWSError) Unwrap() error { return e.Err }

func (c CLI) Call(ctx context.Context, service, operation string, input, output any) error {
	args := []string{service, operation, "--region", c.Region, "--output", "json", "--no-cli-pager"}
	if c.Profile != "" {
		args = append(args, "--profile", c.Profile)
	}
	if input != nil {
		b, err := json.Marshal(input)
		if err != nil {
			return err
		}
		f, err := os.CreateTemp("", "crane-aws-input-*.json")
		if err != nil {
			return err
		}
		defer os.Remove(f.Name())
		if _, err = f.Write(b); err != nil {
			f.Close()
			return err
		}
		if err = f.Close(); err != nil {
			return err
		}
		args = append(args, "--cli-input-json", "file://"+f.Name())
	}
	cmd := exec.CommandContext(ctx, "aws", args...)
	cmd.Env = append(os.Environ(), "AWS_PAGER=", "AWS_CLI_AUTO_PROMPT=off")
	b, err := cmd.Output()
	if err != nil {
		// AWS errors may contain user-supplied or secret values. Never forward stderr.
		code := "command failed"
		if e, ok := err.(*exec.ExitError); ok {
			if match := regexp.MustCompile(`\(([A-Za-z0-9_.-]+)\)`).FindSubmatch(e.Stderr); len(match) == 2 {
				code = string(match[1])
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &AWSError{Service: service, Operation: operation, Code: code, Err: err}
	}
	if output != nil && len(b) > 0 {
		if err := json.Unmarshal(b, output); err != nil {
			return fmt.Errorf("decode AWS %s %s: %w", service, operation, err)
		}
	}
	return nil
}

type Infrastructure struct {
	SpireAddress, SpireLoadBalancerARN, SpireTargetGroupARN           string
	SpireLoadBalancerSecurityGroupID                                  string
	VPCID                                                             string
	PublicSubnetIDs, PrivateSubnetIDs                                 []string
	AvailabilityZones                                                 []string
	SpireSecurityGroupID, PlatformSecurityGroupID, RDSSecurityGroupID string
	SpireProfile, PlatformProfile                                     string
	DBEndpoint, DBSecretARN, RuntimeSecretARN                         string
}
