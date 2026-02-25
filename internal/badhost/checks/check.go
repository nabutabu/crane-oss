package checks

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/nabutabu/crane-oss/internal/badhost/problem"
	"github.com/nabutabu/crane-oss/internal/hostcatalog/service"
	"github.com/nabutabu/crane-oss/pkg/api"
)

type Check interface {
	Name() string
	Detect(ctx context.Context, host *api.Host) ([]problem.Problem, error)
}

type Dependencies struct {
	EC2Client   *ec2.Client
	HostCatalog *service.HostCatalogService
}
