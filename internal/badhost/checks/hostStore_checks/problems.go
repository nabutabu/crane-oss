package hoststorechecks

import (
	"context"
	"time"

	"github.com/nabutabu/crane-oss/internal/badhost/problem"
	"github.com/nabutabu/crane-oss/internal/hostcatalog/service"
	"github.com/nabutabu/crane-oss/pkg/api"
)

type UnhealthyEC2Instance struct {
	catalog *service.HostCatalogService
}

func Name() string {
	return "host-store-check"
}

func Detect(ctx context.Context, host *api.Host) ([]problem.Problem, error) {
	if host.Health == api.HostHealthUnhealthy {
		var p *problem.Problem = &problem.Problem{
			Host_id:    host.ID,
			Type:       problem.ProblemTypeSEL,
			Severity:   problem.SeverityCritical,
			DetectedAt: time.Now(),
		}

		return []problem.Problem{*p}, nil
	}
	return nil, nil
}
