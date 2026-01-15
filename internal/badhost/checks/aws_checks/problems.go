package aws_checks

import (
	"context"

	"github.com/nabutabu/crane-oss/internal/badhost/problem"
	"github.com/nabutabu/crane-oss/pkg/api"
)

type UnhealthyEC2Instance struct {
}

func (c *UnhealthyEC2Instance) Name() string {
	return "aws-ec2-unhealthy"
}

func (ec2Instance *UnhealthyEC2Instance) Detect(ctx context.Context, host *api.Host) ([]problem.Problem, error) {
	return nil, nil
}
