package awscompute

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/aws/smithy-go"
	"github.com/nabutabu/crane-oss/internal/provider"
	"github.com/nabutabu/crane-oss/pkg/api"
)

type runtimeEC2 interface {
	DescribeInstances(context.Context, *ec2.DescribeInstancesInput, ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
	RunInstances(context.Context, *ec2.RunInstancesInput, ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error)
	DescribeImages(context.Context, *ec2.DescribeImagesInput, ...func(*ec2.Options)) (*ec2.DescribeImagesOutput, error)
	DescribeSubnets(context.Context, *ec2.DescribeSubnetsInput, ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error)
	DescribeSecurityGroups(context.Context, *ec2.DescribeSecurityGroupsInput, ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error)
}

type runtimeSSM interface {
	DescribeInstanceInformation(context.Context, *ssm.DescribeInstanceInformationInput, ...func(*ssm.Options)) (*ssm.DescribeInstanceInformationOutput, error)
	SendCommand(context.Context, *ssm.SendCommandInput, ...func(*ssm.Options)) (*ssm.SendCommandOutput, error)
	GetCommandInvocation(context.Context, *ssm.GetCommandInvocationInput, ...func(*ssm.Options)) (*ssm.GetCommandInvocationOutput, error)
}

type Runtime struct {
	ec2         runtimeEC2
	ssm         runtimeSSM
	region      string
	trustDomain string
}

func NewRuntime(ctx context.Context, region, trustDomain string) (*Runtime, error) {
	if region == "" || trustDomain == "" {
		return nil, errors.New("AWS runtime requires region and SPIFFE trust domain")
	}
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, errors.New("load AWS runtime configuration")
	}
	return &Runtime{ec2: ec2.NewFromConfig(cfg), ssm: ssm.NewFromConfig(cfg), region: region, trustDomain: trustDomain}, nil
}

var _ provider.RuntimeProvisioner = (*Runtime)(nil)

type runtimeLaunchParameters struct {
	ImageID           string   `json:"image_id"`
	InstanceType      string   `json:"instance_type"`
	SubnetID          string   `json:"subnet_id"`
	SecurityGroupIDs  []string `json:"security_group_ids"`
	InstanceProfile   string   `json:"iam_instance_profile"`
	AssociatePublicIP *bool    `json:"associate_public_ip_address,omitempty"`
}

func (r *Runtime) LaunchWorker(ctx context.Context, op api.Operation) (provider.RuntimeHost, error) {
	var result provider.RuntimeHost
	var p runtimeLaunchParameters
	d := json.NewDecoder(bytes.NewReader(op.LaunchParameters))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil || p.ImageID == "" || p.InstanceType == "" || p.SubnetID == "" || len(p.SecurityGroupIDs) == 0 || p.InstanceProfile == "" {
		return result, errors.New("runtime launch template is incomplete")
	}
	if op.Provider != "aws" || op.Region != r.region || op.ProviderZoneID == "" || op.ProviderToken == "" || op.HostID == "" {
		return result, errors.New("runtime operation does not match AWS deployment")
	}
	metadata, err := r.imageMetadata(ctx, p.ImageID)
	if err != nil {
		return result, err
	}
	if metadata["CraneImage"] != "v2" || metadata["SpireServerAddress"] == "" || metadata["SpireTrustDomain"] != r.trustDomain || metadata["SpireBundleParameter"] == "" || metadata["AWSRegion"] != r.region {
		return result, errors.New("worker image is not prepared for this deployment")
	}
	deploymentName, ok := deploymentNameFromBundleParameter(metadata["SpireBundleParameter"])
	if !ok || p.InstanceProfile != deploymentName+"-worker" || op.Scheduling.InstanceType != p.InstanceType || op.Scheduling.Region != r.region || op.Scheduling.Zone != op.ProviderZoneID {
		return result, errors.New("worker template does not match the deployment zone")
	}
	subnets, err := r.ec2.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{SubnetIds: []string{p.SubnetID}})
	if err != nil || len(subnets.Subnets) != 1 || aws.ToString(subnets.Subnets[0].AvailabilityZone) != op.ProviderZoneID {
		return result, errors.New("worker subnet is not in the selected availability zone")
	}
	groups, err := r.ec2.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{GroupIds: p.SecurityGroupIDs})
	if err != nil || len(groups.SecurityGroups) != len(p.SecurityGroupIDs) {
		return result, errors.New("worker security groups are unavailable")
	}
	for _, group := range groups.SecurityGroups {
		if aws.ToString(group.VpcId) != aws.ToString(subnets.Subnets[0].VpcId) {
			return result, errors.New("worker security group is outside the selected VPC")
		}
	}
	publicIP := true
	if p.AssociatePublicIP != nil {
		publicIP = *p.AssociatePublicIP
	}
	out, err := r.ec2.RunInstances(ctx, &ec2.RunInstancesInput{
		ImageId: aws.String(p.ImageID), InstanceType: types.InstanceType(p.InstanceType), MinCount: aws.Int32(1), MaxCount: aws.Int32(1),
		ClientToken:        aws.String(op.ProviderToken),
		IamInstanceProfile: &types.IamInstanceProfileSpecification{Name: aws.String(p.InstanceProfile)},
		MetadataOptions:    &types.InstanceMetadataOptionsRequest{HttpTokens: types.HttpTokensStateRequired, HttpPutResponseHopLimit: aws.Int32(2)},
		NetworkInterfaces:  []types.InstanceNetworkInterfaceSpecification{{DeviceIndex: aws.Int32(0), SubnetId: aws.String(p.SubnetID), Groups: p.SecurityGroupIDs, AssociatePublicIpAddress: aws.Bool(publicIP)}},
		TagSpecifications: []types.TagSpecification{{ResourceType: types.ResourceTypeInstance, Tags: []types.Tag{
			{Key: aws.String("CraneBootstrap"), Value: aws.String(deploymentName)},
			{Key: aws.String("BootstrapRole"), Value: aws.String("worker")},
			{Key: aws.String("CraneHostID"), Value: aws.String(op.HostID)},
			{Key: aws.String("CraneOperationID"), Value: aws.String(op.ID)},
			{Key: aws.String("CraneNodeGroupID"), Value: aws.String(op.NodeGroupID)},
			{Key: aws.String("CraneZoneID"), Value: aws.String(op.ZoneID)},
			{Key: aws.String("CraneClusterID"), Value: aws.String(op.ClusterID)},
			{Key: aws.String("CraneNodePoolID"), Value: aws.String(op.NodePoolID)},
			{Key: aws.String("CraneTemplateRevision"), Value: aws.String(op.TemplateRevision)},
			{Key: aws.String("Name"), Value: aws.String("crane-" + op.CapacityClass + "-" + op.HostID)},
		}}},
	})
	if err != nil {
		return result, errors.New("AWS worker launch failed")
	}
	if len(out.Instances) != 1 || out.Instances[0].InstanceId == nil {
		return result, errors.New("AWS worker launch returned no instance")
	}
	result.ProviderID = aws.ToString(out.Instances[0].InstanceId)
	result.KubernetesProviderID = fmt.Sprintf("aws:///%s/%s", op.ProviderZoneID, result.ProviderID)
	result.PrivateIP = aws.ToString(out.Instances[0].PrivateIpAddress)
	if result.ProviderID == "" || result.PrivateIP == "" {
		return provider.RuntimeHost{}, errors.New("AWS worker launch returned incomplete identity")
	}
	return result, nil
}

func deploymentNameFromBundleParameter(parameter string) (string, bool) {
	parts := strings.Split(strings.Trim(parameter, "/"), "/")
	if len(parts) != 3 || parts[0] != "crane" || parts[2] != "spire-bundle" || !deploymentNameRE.MatchString(parts[1]) {
		return "", false
	}
	return parts[1], true
}

var deploymentNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func (r *Runtime) imageMetadata(ctx context.Context, id string) (map[string]string, error) {
	out, err := r.ec2.DescribeImages(ctx, &ec2.DescribeImagesInput{ImageIds: []string{id}})
	if err != nil || len(out.Images) != 1 || out.Images[0].State != types.ImageStateAvailable {
		return nil, errors.New("worker golden image is unavailable")
	}
	metadata := make(map[string]string, len(out.Images[0].Tags))
	for _, tag := range out.Images[0].Tags {
		metadata[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	return metadata, nil
}

func (r *Runtime) ReconcileSubdRegistration(ctx context.Context, op api.Operation, providerID string) error {
	if op.SpireInstanceID == "" || providerID == "" || op.AccountID == "" || op.Region == "" {
		return errors.New("SPIRE registration target is incomplete")
	}
	parent := "spiffe://" + r.trustDomain + "/spire/agent/aws_iid/" + op.AccountID + "/" + op.Region + "/" + providerID
	identity := "spiffe://" + r.trustDomain + "/subd/" + providerID
	script := fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
export PATH=/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin
container=crane-spire-server
socket=/tmp/spire-server/private/api.sock
id=%s
parent=%s
all="$(docker exec "$container" /opt/spire/bin/spire-server entry show -socketPath "$socket" -spiffeID "$id")"
count="$(printf '%%s\n' "$all" | awk '/Entry ID/{n++} END{print n+0}')"
if [ "$count" -gt 1 ]; then exit 1; fi
if [ "$count" = 1 ]; then
  docker exec "$container" /opt/spire/bin/spire-server entry show -socketPath "$socket" -spiffeID "$id" -parentID "$parent" -selector systemd:id:subd.service -matchSelectorsOn exact | grep -q 'Entry ID'
else
  docker exec "$container" /opt/spire/bin/spire-server entry create -socketPath "$socket" -parentID "$parent" -spiffeID "$id" -selector systemd:id:subd.service
fi
`, shellQuote(identity), shellQuote(parent))
	return r.runSSMScript(ctx, op.SpireInstanceID, "subd-registration", script)
}

func (r *Runtime) ConfigureSubd(ctx context.Context, op api.Operation, providerID, privateIP string) error {
	if providerID == "" || privateIP == "" || op.ZoneID == "" {
		return errors.New("worker configuration target is incomplete")
	}
	imageID := ""
	var p runtimeLaunchParameters
	if json.Unmarshal(op.LaunchParameters, &p) == nil {
		imageID = p.ImageID
	}
	metadata, err := r.imageMetadata(ctx, imageID)
	if err != nil {
		return err
	}
	data := scriptData{TrustDomain: r.trustDomain, SpireAddress: metadata["SpireServerAddress"], ZoneID: op.ZoneID,
		Host: bootstrapAgentHost{ID: providerID, PrivateIP: privateIP, Role: "worker", AMI: imageID}}
	script, err := renderTemplate("subd.sh.tmpl", data)
	if err != nil {
		return errors.New("render worker configuration")
	}
	return r.runSSMScript(ctx, providerID, "subd-"+op.ID, script)
}

func (r *Runtime) runSSMScript(ctx context.Context, instance, label, script string) error {
	if !instanceIDRE.MatchString(instance) {
		return errors.New("invalid SSM target identity")
	}
	for {
		out, err := r.ssm.DescribeInstanceInformation(ctx, &ssm.DescribeInstanceInformationInput{Filters: []ssmtypes.InstanceInformationStringFilter{{Key: aws.String("InstanceIds"), Values: []string{instance}}}})
		if err != nil {
			return errors.New("SSM worker discovery failed")
		}
		if len(out.InstanceInformationList) == 1 && out.InstanceInformationList[0].PingStatus == ssmtypes.PingStatusOnline {
			break
		}
		if err := pause(ctx, 10*time.Second); err != nil {
			return err
		}
	}
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	if _, err := zw.Write([]byte(script)); err != nil {
		return errors.New("compress SSM script")
	}
	if err := zw.Close(); err != nil {
		return errors.New("compress SSM script")
	}
	encoded := base64.StdEncoding.EncodeToString(compressed.Bytes())
	command := "set -eu\ninstall -d -m 0755 /var/lib/crane\nprintf %s " + shellQuote(encoded) + " | base64 -d | gzip -d > " + shellQuote("/var/lib/crane/"+label+".sh") + "\nbash " + shellQuote("/var/lib/crane/"+label+".sh")
	if len(command) > 23000 {
		return errors.New("SSM configuration command exceeds the supported size")
	}
	sent, err := r.ssm.SendCommand(ctx, &ssm.SendCommandInput{DocumentName: aws.String("AWS-RunShellScript"), InstanceIds: []string{instance}, Parameters: map[string][]string{"commands": {command}}, Comment: aws.String("Crane runtime host configuration")})
	if err != nil || sent.Command == nil || sent.Command.CommandId == nil {
		return errors.New("SSM configuration command submission failed")
	}
	commandID := aws.ToString(sent.Command.CommandId)
	for {
		invocation, err := r.ssm.GetCommandInvocation(ctx, &ssm.GetCommandInvocationInput{CommandId: aws.String(commandID), InstanceId: aws.String(instance)})
		if err != nil {
			if isSmithyCode(err, "InvocationDoesNotExist") {
				if err := pause(ctx, 2*time.Second); err != nil {
					return err
				}
				continue
			}
			return errors.New("SSM configuration status unavailable")
		}
		switch string(invocation.Status) {
		case "Success":
			return nil
		case "Failed", "Cancelled", "TimedOut", "DeliveryTimedOut", "ExecutionTimedOut", "Undeliverable", "Terminated":
			return errors.New("SSM host configuration failed")
		default:
			if err := pause(ctx, 3*time.Second); err != nil {
				return err
			}
		}
	}
}

func isSmithyCode(err error, code string) bool {
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) && strings.EqualFold(apiErr.ErrorCode(), code)
}
