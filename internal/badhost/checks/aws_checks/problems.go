package aws_checks

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/nabutabu/crane-oss/internal/badhost/problem"
	"github.com/nabutabu/crane-oss/pkg/api"
)

type UnhealthyEC2Instance struct {
	Client *ec2.Client
}

func (c *UnhealthyEC2Instance) Name() string {
	return "aws-ec2-unhealthy"
}

func (ec2Instance *UnhealthyEC2Instance) Detect(ctx context.Context, host *api.Host) ([]problem.Problem, error) {
	input := &ec2.DescribeInstanceStatusInput{
		InstanceIds: []string{host.ProviderID},
		// Optional: IncludeAllInstances can be set to false if only a specific instance is needed
		IncludeAllInstances: aws.Bool(true),
	}

	out, err := ec2Instance.Client.DescribeInstanceStatus(ctx, input)
	if err != nil {
		return nil, err
	}
	if len(out.InstanceStatuses) == 0 {
		return nil, nil
	}

	status := out.InstanceStatuses[0]

	unhealthy := status.SystemStatus == nil ||
		status.InstanceStatus == nil ||
		status.SystemStatus.Status != types.SummaryStatusOk ||
		status.InstanceStatus.Status != types.SummaryStatusOk

	if !unhealthy {
		return nil, nil
	}

	var p *problem.Problem = &problem.Problem{
		Host_id:    host.ID,
		Type:       problem.ProblemTypeSEL,
		Severity:   problem.SeverityCritical,
		DetectedAt: time.Now(),
	}

	return []problem.Problem{*p}, nil
}
