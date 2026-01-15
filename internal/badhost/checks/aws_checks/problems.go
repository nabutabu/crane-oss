package aws_checks

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/nabutabu/crane-oss/internal/badhost/problem"
	"github.com/nabutabu/crane-oss/pkg/api"
)

type UnhealthyEC2Instance struct {
	client *ec2.Client
}

func (c *UnhealthyEC2Instance) Name() string {
	return "aws-ec2-unhealthy"
}

func (ec2Instance *UnhealthyEC2Instance) Detect(ctx context.Context, host *api.Host) ([]problem.Problem, error) {
	input := &ec2.DescribeInstanceStatusInput{
		InstanceIds: []string{host.ProviderID},
		// Optional: IncludeAllInstances can be set to false if only a specific instance is needed
		IncludeAllInstances: &[]bool{true}[0],
	}

	_, err := ec2Instance.client.DescribeInstanceStatus(ctx, input)
	if err != nil {
		return nil, err
	}

	return nil, nil
}
