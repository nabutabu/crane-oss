package awscompute

import (
	"context"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/nabutabu/crane-oss/pkg/api"
	"testing"
)

type identityEC2 struct {
	runtimeEC2
	output *ec2.DescribeInstancesOutput
}

func (f identityEC2) DescribeInstances(context.Context, *ec2.DescribeInstancesInput, ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	return f.output, nil
}

func TestVerifyWorkerRejectsForgedPlacementAndOwnership(t *testing.T) {
	op := api.Operation{ID: "operation", HostID: "host", Provider: "aws", AccountID: "account", Region: "us-west-2", ProviderZoneID: "us-west-2a", ZoneID: "zone", ClusterID: "cluster", NodeGroupID: "group", NodePoolID: "pool", TemplateRevision: "revision", LaunchParameters: []byte(`{"image_id":"ami-image","instance_type":"m7i.xlarge","subnet_id":"subnet","iam_instance_profile":"deployment-worker"}`)}
	fixture := func() *ec2.DescribeInstancesOutput {
		instance := types.Instance{InstanceId: aws.String("i-host"), PrivateIpAddress: aws.String("10.0.0.8"), Placement: &types.Placement{AvailabilityZone: aws.String("us-west-2a")}, SubnetId: aws.String("subnet"), ImageId: aws.String("ami-image"), InstanceType: types.InstanceTypeM7iXlarge, State: &types.InstanceState{Name: types.InstanceStateNameRunning}, IamInstanceProfile: &types.IamInstanceProfile{Arn: aws.String("arn:aws:iam::account:instance-profile/deployment-worker")}}
		for k, v := range map[string]string{"CraneHostID": "host", "CraneOperationID": "operation", "CraneNodeGroupID": "group", "CraneZoneID": "zone", "CraneClusterID": "cluster", "CraneNodePoolID": "pool", "CraneTemplateRevision": "revision", "BootstrapRole": "worker", "CraneBootstrap": "deployment"} {
			instance.Tags = append(instance.Tags, types.Tag{Key: aws.String(k), Value: aws.String(v)})
		}
		return &ec2.DescribeInstancesOutput{Reservations: []types.Reservation{{OwnerId: aws.String("account"), Instances: []types.Instance{instance}}}}
	}
	for _, tc := range []struct {
		name   string
		mutate func(*ec2.DescribeInstancesOutput)
	}{
		{"valid", nil},
		{"wrong account", func(out *ec2.DescribeInstancesOutput) { out.Reservations[0].OwnerId = aws.String("forged") }},
		{"wrong zone", func(out *ec2.DescribeInstancesOutput) {
			out.Reservations[0].Instances[0].Placement.AvailabilityZone = aws.String("unknown")
		}},
		{"forged ownership", func(out *ec2.DescribeInstancesOutput) { out.Reservations[0].Instances[0].Tags = nil }},
		{"public address", func(out *ec2.DescribeInstancesOutput) {
			out.Reservations[0].Instances[0].PrivateIpAddress = aws.String("8.8.8.8")
		}},
		{"duplicate instances", func(out *ec2.DescribeInstancesOutput) {
			out.Reservations[0].Instances = append(out.Reservations[0].Instances, out.Reservations[0].Instances[0])
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := fixture()
			if tc.mutate != nil {
				tc.mutate(out)
			}
			r := Runtime{ec2: identityEC2{output: out}, region: op.Region}
			identity, err := r.VerifyWorker(context.Background(), op, "i-host")
			if tc.mutate == nil {
				if err != nil || identity.KubernetesProviderID != "aws:///us-west-2a/i-host" {
					t.Fatal(identity, err)
				}
			} else if err == nil {
				t.Fatal("forged identity accepted")
			}
		})
	}
}
