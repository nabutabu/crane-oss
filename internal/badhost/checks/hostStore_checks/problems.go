package hoststorechecks

import (
	"context"
	"log"
	"time"

	"github.com/nabutabu/crane-oss/internal/badhost/problem"
	"github.com/nabutabu/crane-oss/internal/hostcatalog/service"
	"github.com/nabutabu/crane-oss/pkg/api"
)

type UnhealthyHostStoreCheck struct {
	catalog *service.HostCatalogService
}

func NewUnhealthyHostStoreCheck(catalog *service.HostCatalogService) *UnhealthyHostStoreCheck {
	return &UnhealthyHostStoreCheck {
		catalog: catalog,
	}
}


func (check *UnhealthyHostStoreCheck) Name() string {
	return "host.store.check"
}

func (check *UnhealthyHostStoreCheck) Detect(ctx context.Context, host *api.Host) ([]problem.Problem, error) {
	log.Println("/hostStore/Detect")
	var problems []problem.Problem

	if host.Health == api.HostHealthUnhealthy {
		p := problem.Problem{
			Host_id:    host.ID,
			Type:       problem.ProblemTypeSEL,
			Severity:   problem.SeverityCritical,
			DetectedAt: time.Now(),
		}
		problems = append(problems, p)
	}

	timeSinceHeartbeat := time.Since(host.LastSeenHeartbeat)
	if host.LastSeenHeartbeat.IsZero() {
		timeSinceHeartbeat = time.Since(host.CreatedAt)
	}

	if timeSinceHeartbeat > 3*time.Minute {
		p := problem.Problem{
			Host_id:    host.ID,
			Type:       problem.ProblemTypeNoHeartbeat,
			Severity:   problem.SeverityCritical,
			DetectedAt: time.Now(),
		}
		problems = append(problems, p)
	}

	if len(problems) > 0 {
		return problems, nil
	}

	return nil, nil
}
