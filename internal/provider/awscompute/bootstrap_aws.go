package awscompute

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"time"

	"github.com/nabutabu/crane-oss/internal/bootstrap"
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
	started := time.Now()
	label := resourceLabel(service, operation, input)
	reporter := bootstrap.ReporterFromContext(ctx)
	if reporter != nil {
		_ = reporter.Detail(service, "checking "+label+"; waiting for AWS "+service+" "+operation+" to return")
		stop := make(chan struct{})
		defer close(stop)
		go func() {
			ticker := time.NewTicker(10 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-stop:
					return
				case <-ctx.Done():
					return
				case <-ticker.C:
					_ = reporter.Detail(service, "still waiting for AWS "+service+" "+operation+" for "+label+" ["+time.Since(started).Round(time.Second).String()+"]")
				}
			}
		}()
		defer func() {
			_ = reporter.Detail(service, "AWS "+service+" "+operation+" returned for "+label+" ["+time.Since(started).Round(time.Second).String()+"]")
		}()
	}
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

// Only allowlisted identifiers are displayed. AWS requests can contain secrets.
func resourceLabel(service, operation string, input any) string {
	label := service + " resource"
	if operation == "get-secret-value" || operation == "create-secret" {
		return "runtime secret"
	}
	fields, ok := input.(map[string]any)
	if !ok {
		return label
	}
	for _, key := range []string{"DBInstanceIdentifier", "DBSubnetGroupName", "RoleName", "InstanceProfileName", "VpcId", "SubnetId", "GroupId", "CommandId"} {
		if value, ok := fields[key].(string); ok && value != "" {
			return label + " " + value
		}
	}
	return label
}

type Infrastructure struct {
	SpireAddress, SpireLoadBalancerARN, SpireTargetGroupARN           string
	SpireLoadBalancerSecurityGroupID                                  string
	VPCID                                                             string
	PublicSubnetIDs, PrivateSubnetIDs                                 []string
	AvailabilityZones                                                 []string
	SpireSecurityGroupID, PlatformSecurityGroupID, RDSSecurityGroupID string
	SpireProfile, PlatformProfile, WorkerProfile                      string
	DBEndpoint, DBSecretARN, RuntimeSecretARN                         string
}
