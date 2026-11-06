package awscompute

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/nabutabu/crane-oss/internal/capacity"
	"github.com/nabutabu/crane-oss/pkg/api"
)

// ResolveAttachmentTemplate pins a zone-local worker template from an active
// deployment group. The attach request cannot provide an image or IAM role.
func ResolveAttachmentTemplate(ctx context.Context, deployment capacity.DeploymentRuntime, source api.NodeGroup, zone api.ZoneInput, publicSubnetID string) (json.RawMessage, string, api.SchedulingTemplate, error) {
	invalid := errors.New("deployment worker template is unavailable")
	if source.ClusterID != deployment.ClusterID || source.LifecycleState != "active" ||
		deployment.Provider != "aws" || deployment.Region != zone.Region ||
		publicSubnetID == "" || source.TemplateRevision == "" {
		return nil, "", api.SchedulingTemplate{}, invalid
	}
	runtime, err := NewRuntime(ctx, deployment.Region, deployment.TrustDomain)
	if err != nil {
		return nil, "", api.SchedulingTemplate{}, err
	}
	return runtime.resolveAttachmentTemplate(ctx, deployment, source, zone, publicSubnetID)
}

func (r *Runtime) resolveAttachmentTemplate(ctx context.Context, deployment capacity.DeploymentRuntime, source api.NodeGroup, zone api.ZoneInput, publicSubnetID string) (json.RawMessage, string, api.SchedulingTemplate, error) {
	invalid := errors.New("deployment worker template is unavailable")
	var launch runtimeLaunchParameters
	decoder := json.NewDecoder(bytes.NewReader(source.LaunchParameters))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&launch) != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		launch.ImageID == "" || launch.InstanceType == "" || launch.SubnetID == "" ||
		len(launch.SecurityGroupIDs) == 0 || launch.InstanceProfile != deployment.DeploymentID+"-worker" ||
		source.Scheduling.InstanceType != launch.InstanceType || source.Scheduling.Region != deployment.Region ||
		source.Scheduling.Zone != source.ProviderZoneID || source.ProviderZoneID == "" {
		return nil, "", api.SchedulingTemplate{}, invalid
	}
	metadata, err := r.imageMetadata(ctx, launch.ImageID)
	if err != nil || metadata["CraneImage"] != "v2" || metadata["SpireTrustDomain"] != deployment.TrustDomain ||
		metadata["AWSRegion"] != deployment.Region || metadata["SpireServerAddress"] == "" ||
		metadata["SpireBundleParameter"] != "/crane/"+deployment.DeploymentID+"/spire-bundle" {
		return nil, "", api.SchedulingTemplate{}, invalid
	}
	subnets, err := r.ec2.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{SubnetIds: []string{launch.SubnetID, publicSubnetID}})
	if err != nil || len(subnets.Subnets) != 2 {
		return nil, "", api.SchedulingTemplate{}, invalid
	}
	matched, sourceMatched := false, false
	for _, subnet := range subnets.Subnets {
		if aws.ToString(subnet.VpcId) != deployment.VPCID {
			return nil, "", api.SchedulingTemplate{}, invalid
		}
		if aws.ToString(subnet.SubnetId) == publicSubnetID && aws.ToString(subnet.AvailabilityZone) == zone.ProviderZoneID &&
			tagValue(subnet.Tags, "CraneBootstrap") == deployment.DeploymentID {
			matched = true
		}
		if aws.ToString(subnet.SubnetId) == launch.SubnetID && aws.ToString(subnet.AvailabilityZone) == source.ProviderZoneID {
			sourceMatched = true
		}
	}
	if !matched || !sourceMatched {
		return nil, "", api.SchedulingTemplate{}, invalid
	}
	groups, err := r.ec2.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{GroupIds: launch.SecurityGroupIDs})
	if err != nil || len(groups.SecurityGroups) != len(launch.SecurityGroupIDs) {
		return nil, "", api.SchedulingTemplate{}, invalid
	}
	for _, group := range groups.SecurityGroups {
		if aws.ToString(group.VpcId) != deployment.VPCID || tagValue(group.Tags, "CraneBootstrap") != deployment.DeploymentID {
			return nil, "", api.SchedulingTemplate{}, invalid
		}
	}
	launch.SubnetID = publicSubnetID
	public := true
	launch.AssociatePublicIP = &public
	encoded, err := json.Marshal(launch)
	if err != nil {
		return nil, "", api.SchedulingTemplate{}, invalid
	}
	hash := sha256.Sum256(encoded)
	revision := "attach-" + hex.EncodeToString(hash[:8])
	scheduling := source.Scheduling
	scheduling.Region = zone.Region
	scheduling.Zone = zone.ProviderZoneID
	if scheduling.Labels != nil {
		labels := make(map[string]string, len(scheduling.Labels))
		for key, value := range scheduling.Labels {
			labels[key] = value
		}
		if _, ok := labels["topology.kubernetes.io/region"]; ok {
			labels["topology.kubernetes.io/region"] = zone.Region
		}
		if _, ok := labels["topology.kubernetes.io/zone"]; ok {
			labels["topology.kubernetes.io/zone"] = zone.ProviderZoneID
		}
		scheduling.Labels = labels
	}
	return encoded, revision, scheduling, nil
}
