package aws_checks

import (
	"context"

	"github.com/nabutabu/crane-oss/internal/badhost"
	"github.com/nabutabu/crane-oss/pkg/api"
)

type UnhealthyEC2Instance struct {
}

func (c *UnhealthyEC2Instance) Name() string {
	return "aws-ec2-unhealthy"
}

func (ec2Instance *UnhealthyEC2Instance) checkEC2Health(ctx context.Context, host *api.Host) ([]badhost.Problem, error) {
	return nil, nil
}
