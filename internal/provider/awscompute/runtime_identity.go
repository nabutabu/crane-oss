package awscompute

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/nabutabu/crane-oss/internal/provider"
	"github.com/nabutabu/crane-oss/pkg/api"
	"net"
	"strings"
)

func (r *Runtime) VerifyWorker(ctx context.Context, op api.Operation, id string) (provider.RuntimeIdentity, error) {
	invalid := errors.New("provider worker identity verification failed")
	if id == "" || op.Provider != "aws" || op.Region != r.region || op.AccountID == "" || op.ClusterID == "" || op.NodePoolID == "" || op.HostID == "" || op.ID == "" || op.NodeGroupID == "" || op.ZoneID == "" || op.TemplateRevision == "" {
		return provider.RuntimeIdentity{}, invalid
	}
	out, err := r.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{id}})
	if err != nil || len(out.Reservations) != 1 || aws.ToString(out.Reservations[0].OwnerId) != op.AccountID || len(out.Reservations[0].Instances) != 1 {
		return provider.RuntimeIdentity{}, invalid
	}
	instance := out.Reservations[0].Instances[0]
	var template runtimeLaunchParameters
	if json.Unmarshal(op.LaunchParameters, &template) != nil || instance.Placement == nil || instance.State == nil || instance.State.Name != types.InstanceStateNameRunning || aws.ToString(instance.InstanceId) != id || aws.ToString(instance.Placement.AvailabilityZone) != op.ProviderZoneID || aws.ToString(instance.SubnetId) != template.SubnetID || aws.ToString(instance.ImageId) != template.ImageID || string(instance.InstanceType) != template.InstanceType || instance.IamInstanceProfile == nil || !strings.HasSuffix(aws.ToString(instance.IamInstanceProfile.Arn), "instance-profile/"+template.InstanceProfile) {
		return provider.RuntimeIdentity{}, invalid
	}
	tags := map[string]string{}
	for _, t := range instance.Tags {
		tags[aws.ToString(t.Key)] = aws.ToString(t.Value)
	}
	for key, want := range map[string]string{"CraneHostID": op.HostID, "CraneOperationID": op.ID, "CraneNodeGroupID": op.NodeGroupID, "CraneZoneID": op.ZoneID, "CraneClusterID": op.ClusterID, "CraneNodePoolID": op.NodePoolID, "CraneTemplateRevision": op.TemplateRevision, "BootstrapRole": "worker"} {
		if tags[key] != want {
			return provider.RuntimeIdentity{}, invalid
		}
	}
	if tags["CraneBootstrap"] == "" || template.InstanceProfile != tags["CraneBootstrap"]+"-worker" {
		return provider.RuntimeIdentity{}, invalid
	}
	ip := aws.ToString(instance.PrivateIpAddress)
	parsed := net.ParseIP(ip)
	if parsed == nil || !parsed.IsPrivate() {
		return provider.RuntimeIdentity{}, invalid
	}
	return provider.RuntimeIdentity{PrivateIP: ip, KubernetesProviderID: fmt.Sprintf("aws:///%s/%s", op.ProviderZoneID, id)}, nil
}
