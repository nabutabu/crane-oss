package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nabutabu/crane-oss/internal/logging"
)

type Result struct {
	Name               string `json:"name"`
	Region             string `json:"region"`
	SpireInstanceID    string `json:"spire_instance_id"`
	PlatformInstanceID string `json:"platform_instance_id"`
	SpireAddress       string `json:"spire_address"`
	PlatformAddress    string `json:"platform_address"`
	APIServerEndpoint  string `json:"api_server_endpoint"`
	BundleParameter    string `json:"spire_bundle_parameter"`
	RuntimeSecretARN   string `json:"runtime_secret_arn"`
}

// RunAWS bootstraps protected infrastructure without a pre-existing database.
// State is rediscovered by ownership tags and stable names. The deployment
// local manifest pins deployment inputs across interrupted invocations.
func RunAWS(ctx context.Context, cfg Config, cli CLI, reporter *Reporter, options RunOptions) (Result, error) {
	var result Result
	cfg = cfg.WithDefaults()
	if err := cfg.Validate(); err != nil {
		return result, err
	}
	if cfg.Version != 2 {
		return result, fmt.Errorf("AWS platform provisioning requires bootstrap config version 2")
	}
	if _, err := exec.LookPath("aws"); err != nil {
		return result, fmt.Errorf("aws must be installed on the operator machine")
	}
	if !options.PrepareImages && (cfg.SPIRE.AMIID == "" || cfg.Platform.AMIID == "" || cfg.Platform.CraneAPIImage == "" || cfg.Platform.DominatorImage == "") {
		return result, fmt.Errorf("provide golden spire.ami_id/platform.ami_id and pinned platform.crane_api_image/platform.dominator_image; run -prepare-images and build the AMIs first")
	}
	if options.StateDir == "" {
		return result, fmt.Errorf("state directory is required")
	}
	if err := os.MkdirAll(options.StateDir, 0700); err != nil {
		return result, err
	}
	phases := AWSPhases(options.PrepareImages)
	step := func(name string, fn func() (string, error)) error { return reporter.Run(len(phases), name, fn) }
	var err error
	if err = step("validate AWS identity", func() (string, error) {
		var identity struct{ Account string }
		if e := cli.Call(ctx, "sts", "get-caller-identity", nil, &identity); e != nil {
			return "", e
		}
		if identity.Account == "" {
			return "", fmt.Errorf("AWS identity returned no account")
		}
		if cfg.AWS.AccountID != "" && cfg.AWS.AccountID != identity.Account {
			return "", fmt.Errorf("configured AWS account does not match the credential identity")
		}
		if e := reporter.Detail("identity", "validated AWS account "+identity.Account); e != nil {
			return "", e
		}
		cfg.AWS.AccountID = identity.Account
		return "account " + identity.Account, nil
	}); err != nil {
		return result, err
	}
	if err = step("persist deployment configuration locally", func() (string, error) { return "", pinDeployment(ctx, cli, &cfg, options.StateDir) }); err != nil {
		return result, err
	}
	var infra Infrastructure
	if options.PrepareImages {
		if err = step("ensure network and private SPIRE endpoint", func() (string, error) {
			var e error
			infra, e = ensureNetwork(ctx, cli, cfg)
			if e != nil {
				return "", e
			}
			infra, e = ensureSecurityGroups(ctx, cli, cfg, infra)
			if e != nil {
				return "", e
			}
			infra, e = ensureSpireEndpoint(ctx, cli, cfg, infra)
			return "", e
		}); err != nil {
			return result, err
		}
		result = Result{Name: cfg.Name, Region: cfg.AWS.Region, SpireAddress: infra.SpireAddress, BundleParameter: bundleParameter(cfg)}
		err = step("persist bootstrap result locally", func() (string, error) {
			return "endpoint prepared for image generation", writeJSONAtomic(filepath.Join(options.StateDir, "endpoint.json"), result)
		})
		return result, err
	}
	if err = step("ensure runtime secret", func() (string, error) {
		var e error
		infra, e = ensureSecrets(ctx, cli, cfg, Infrastructure{})
		return "", e
	}); err != nil {
		return result, err
	}
	if err = step("ensure network, RDS and runtime roles", func() (string, error) {
		var e error
		infra, e = ensureComputeInfrastructure(ctx, cli, cfg, infra)
		if e == nil {
			e = reporter.Detail("infrastructure", fmt.Sprintf("VPC %s, %d public subnets, %d private subnets, RDS and IAM reconciled", infra.VPCID, len(infra.PublicSubnetIDs), len(infra.PrivateSubnetIDs)))
		}
		return "", e
	}); err != nil {
		return result, err
	}
	var spire, platform host
	if err = step("ensure protected SPIRE and platform hosts", func() (string, error) {
		for _, ami := range []string{cfg.SPIRE.AMIID, cfg.Platform.AMIID} {
			if e := validateGoldenImage(ctx, cli, cfg, infra, ami); e != nil {
				return "", e
			}
		}
		var e error
		spire, e = ensureHost(ctx, cli, cfg, infra, "spire", cfg.SPIRE.AMIID, cfg.SPIRE.InstanceType, cfg.SPIRE.StorageGiB, infra.SpireProfile, infra.SpireSecurityGroupID)
		if e != nil {
			return "", e
		}
		if e = registerSpireTarget(ctx, cli, infra, spire.ID); e != nil {
			return "", e
		}
		platform, e = ensureHost(ctx, cli, cfg, infra, "platform", cfg.Platform.AMIID, cfg.Platform.InstanceType, cfg.Platform.StorageGiB, infra.PlatformProfile, infra.PlatformSecurityGroupID)
		if e == nil {
			e = reporter.Detail("hosts", fmt.Sprintf("SPIRE %s and platform %s are SSM online", spire.ID, platform.ID))
		}
		return "", e
	}); err != nil {
		return result, err
	}
	if err = step("install databases and SPIRE server", func() (string, error) {
		script, e := RenderSpireScript(cfg, infra)
		if e != nil {
			return "", e
		}
		return "", runScript(ctx, cli, reporter, spire.ID, "spire", script)
	}); err != nil {
		return result, err
	}
	if err = step("register platform workload identities", func() (string, error) {
		script, e := RenderIdentityScript(cfg, infra, platform.ID)
		if e != nil {
			return "", e
		}
		return "", runScript(ctx, cli, reporter, spire.ID, "identities", script)
	}); err != nil {
		return result, err
	}
	if err = step("initialize Kubernetes, Cilium and platform services", func() (string, error) {
		script, e := RenderPlatformScript(cfg, infra, infra.SpireAddress)
		if e != nil {
			return "", e
		}
		return "", runScript(ctx, cli, reporter, platform.ID, "platform", script)
	}); err != nil {
		return result, err
	}
	if err = step("validate identity, cluster and service handoff", func() (string, error) {
		script, e := RenderValidateScript(cfg, infra)
		if e != nil {
			return "", e
		}
		return "", runScript(ctx, cli, reporter, platform.ID, "validate", script)
	}); err != nil {
		return result, err
	}
	result = Result{Name: cfg.Name, Region: cfg.AWS.Region, SpireInstanceID: spire.ID, PlatformInstanceID: platform.ID, SpireAddress: infra.SpireAddress, BundleParameter: bundleParameter(cfg), PlatformAddress: platform.PrivateIP, APIServerEndpoint: "https://" + platform.PrivateIP + ":6443", RuntimeSecretARN: infra.RuntimeSecretARN}
	if err = step("persist bootstrap result locally", func() (string, error) {
		return "first platform zone validated", writeJSONAtomic(filepath.Join(options.StateDir, "result.json"), result)
	}); err != nil {
		return result, err
	}
	return result, nil
}

type host struct {
	ID        string
	PrivateIP string
}

func ensureHost(ctx context.Context, aws AWSClient, cfg Config, infra Infrastructure, role, ami, instanceType string, storage int32, profile, sg string) (host, error) {
	var existing struct {
		Reservations []struct {
			Instances []struct {
				InstanceID       string `json:"InstanceId"`
				PrivateIPAddress string
				ImageID          string `json:"ImageId"`
				InstanceType     string
				State            struct{ Name string }
			}
		}
	}
	input := map[string]any{"Filters": []map[string]any{{"Name": "tag:CraneBootstrap", "Values": []string{cfg.Name}}, {"Name": "tag:BootstrapRole", "Values": []string{role}}, {"Name": "instance-state-name", "Values": []string{"pending", "running", "stopped", "stopping"}}}}
	if err := aws.Call(ctx, "ec2", "describe-instances", input, &existing); err != nil {
		return host{}, err
	}
	var found []host
	for _, r := range existing.Reservations {
		for _, i := range r.Instances {
			if i.ImageID != ami || i.InstanceType != instanceType {
				return host{}, fmt.Errorf("existing %s host has an incompatible AMI or instance type", role)
			}
			if i.State.Name == "stopped" || i.State.Name == "stopping" {
				return host{}, fmt.Errorf("existing %s host is stopped; resume it before rerunning bootstrap", role)
			}
			found = append(found, host{ID: i.InstanceID, PrivateIP: i.PrivateIPAddress})
		}
	}
	if len(found) > 1 {
		return host{}, fmt.Errorf("multiple %s hosts have this deployment's ownership tags", role)
	}
	var h host
	if len(found) == 1 {
		h = found[0]
	} else {
		token := sha256.Sum256([]byte(cfg.AWS.AccountID + "/" + cfg.AWS.Region + "/" + cfg.Name + "/" + role))
		var launched struct {
			Instances []struct {
				InstanceID       string `json:"InstanceId"`
				PrivateIPAddress string
			}
		}
		launch := map[string]any{"ImageId": ami, "InstanceType": instanceType, "MinCount": 1, "MaxCount": 1, "ClientToken": hex.EncodeToString(token[:]), "IamInstanceProfile": map[string]string{"Name": profile}, "MetadataOptions": map[string]any{"HttpTokens": "required", "HttpPutResponseHopLimit": 2}, "NetworkInterfaces": []map[string]any{{"DeviceIndex": 0, "SubnetId": infra.PublicSubnetIDs[0], "Groups": []string{sg}, "AssociatePublicIpAddress": true}}, "BlockDeviceMappings": []map[string]any{{"DeviceName": "/dev/sda1", "Ebs": map[string]any{"VolumeSize": storage, "VolumeType": "gp3", "Encrypted": true, "DeleteOnTermination": true}}}, "TagSpecifications": []map[string]any{{"ResourceType": "instance", "Tags": []map[string]string{{"Key": "Name", "Value": cfg.Name + "-" + role}, {"Key": "CraneBootstrap", "Value": cfg.Name}, {"Key": "BootstrapRole", "Value": role}}}, {"ResourceType": "volume", "Tags": []map[string]string{{"Key": "CraneBootstrap", "Value": cfg.Name}}}}}
		for attempt := 0; ; attempt++ {
			err := aws.Call(ctx, "ec2", "run-instances", launch, &launched)
			if err == nil {
				break
			}
			if attempt >= 7 || !isCode(err, "InvalidParameterValue", "InvalidIamInstanceProfile") {
				return h, err
			}
			if err = pause(ctx, 5*time.Second); err != nil {
				return h, err
			}
		}
		if len(launched.Instances) != 1 {
			return h, fmt.Errorf("AWS did not return exactly one %s instance", role)
		}
		h = host{ID: launched.Instances[0].InstanceID, PrivateIP: launched.Instances[0].PrivateIPAddress}
	}
	// SSM becoming online is required before submitting commands to a new host.
	for {
		var out struct {
			InstanceInformationList []struct {
				InstanceID string `json:"InstanceId"`
				PingStatus string
			}
		}
		if err := aws.Call(ctx, "ssm", "describe-instance-information", map[string]any{"Filters": []map[string]any{{"Key": "InstanceIds", "Values": []string{h.ID}}}}, &out); err != nil {
			return h, err
		}
		if len(out.InstanceInformationList) == 1 && out.InstanceInformationList[0].PingStatus == "Online" {
			break
		}
		if err := pause(ctx, 10*time.Second); err != nil {
			return h, err
		}
	}
	if h.PrivateIP == "" {
		var out struct {
			Reservations []struct {
				Instances []struct{ PrivateIPAddress string }
			}
		}
		if err := aws.Call(ctx, "ec2", "describe-instances", map[string]any{"InstanceIds": []string{h.ID}}, &out); err != nil {
			return h, err
		}
		if len(out.Reservations) != 1 || len(out.Reservations[0].Instances) != 1 {
			return h, fmt.Errorf("cannot discover %s host private address", role)
		}
		h.PrivateIP = out.Reservations[0].Instances[0].PrivateIPAddress
	}
	return h, nil
}

func pause(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func runScript(ctx context.Context, cli AWSClient, reporter *Reporter, instance, name, script string) error {
	command := "set -eu\ncloud-init status --wait >/dev/null\ninstall -d -m 0755 /var/lib/crane\n"
	path := "/var/lib/crane/" + name + ".sh"
	body := "set -eu\numask 077\nprintf %s " + shellQuote(base64.StdEncoding.EncodeToString([]byte(script))) + " | base64 -d > " + shellQuote(path) + "\nbash " + shellQuote(path)
	command += "flock -w 3500 /var/lib/crane-bootstrap.lock bash -c " + shellQuote(body)
	if len(command) > 23000 {
		return fmt.Errorf("bootstrap %s exceeds SSM command size; move reusable setup into the golden image", name)
	}

	var out struct {
		Command struct {
			CommandID string `json:"CommandId"`
		}
	}
	input := map[string]any{"DocumentName": "AWS-RunShellScript", "InstanceIds": []string{instance}, "Comment": "Crane bootstrap " + name, "TimeoutSeconds": 3600, "Parameters": map[string][]string{"commands": {command}, "executionTimeout": {"3600"}}}
	if err := cli.Call(ctx, "ssm", "send-command", input, &out); err != nil {
		return err
	}
	if out.Command.CommandID == "" {
		return fmt.Errorf("SSM returned no command id")
	}
	if err := reporter.Detail(name, "SSM command "+out.Command.CommandID+" submitted; waiting for host"); err != nil {
		return err
	}
	for {
		var status struct {
			Status                string
			StandardOutputContent string
			StandardErrorContent  string
		}
		err := cli.Call(ctx, "ssm", "get-command-invocation", map[string]any{"CommandId": out.Command.CommandID, "InstanceId": instance}, &status)
		if err != nil {
			var e *AWSError
			if !errors.As(err, &e) || e.Code != "InvocationDoesNotExist" {
				return err
			}
		} else {
			switch status.Status {
			case "Success":
				if err := reporter.Output(name, "stdout", logging.RedactText(status.StandardOutputContent)); err != nil {
					return err
				}
				if err := reporter.Output(name, "stderr", logging.RedactText(status.StandardErrorContent)); err != nil {
					return err
				}
				return nil
			case "Failed", "Cancelled", "TimedOut", "Cancelling":
				_ = reporter.Output(name, "stdout", logging.RedactText(status.StandardOutputContent))
				_ = reporter.Output(name, "stderr", logging.RedactText(status.StandardErrorContent))
				return fmt.Errorf("bootstrap %s failed on %s (SSM command %s)", name, instance, out.Command.CommandID)
			case "Pending", "InProgress", "Delayed":
			default:
				return fmt.Errorf("SSM command %s returned unexpected state %s", out.Command.CommandID, status.Status)
			}
		}
		if err = pause(ctx, 5*time.Second); err != nil {
			return err
		}
	}
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
