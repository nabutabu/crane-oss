package badhost

import (
	"context"
	"log"
	"time"

	"github.com/nabutabu/crane-oss/internal/hostcatalog/service"
	"github.com/nabutabu/crane-oss/pkg/api"
)

type BadHostDetector struct {
	hostStore    service.HostCatalogService
	problemStore ProblemStore
	definitions  map[ProblemType]ProblemDefinition
	cfg          Config
	registry     *Registry
}

func New(hostStore service.HostCatalogService, cfg Config) *BadHostDetector {
	return &BadHostDetector{
		hostStore: hostStore,
		cfg:       cfg,
	}
}

func (detector *BadHostDetector) Run(ctx context.Context) {
	ticker := time.NewTicker(detector.cfg.ScanInterval)
	defer ticker.Stop()

	log.Printf("[BHD] starting for zone=%s", detector.cfg.Zone)

	for {
		select {
		case <-ctx.Done():
			log.Println("[BHD] shutting down")
			return

		case <-ticker.C:
			detector.ScanZone(ctx, detector.cfg.Zone)
		}
	}
}

func (detector *BadHostDetector) ScanZone(ctx context.Context, zone string) error {
	// 1. Get all hosts in zone from host catalog
	hosts, err := detector.hostStore.GetByZone(ctx, zone)
	if err != nil {
		return err
	}

	// 2. Scan each host for problems
	for _, host := range hosts {
		problems := detector.detectProblems(ctx, host)
		if len(problems) > 0 {
			// 3. Record problems in central database
			for _, problem := range problems {
				detector.problemStore.RecordProblem(ctx, host.ID, *problem)
			}
		}
	}

	// 4. Analyze trends and identify cycling hosts
	// return b.analyzeTrends(ctx, zone)
	return nil
}

func (detector *BadHostDetector) detectProblems(ctx context.Context, host *api.Host) []*Problem {
	var problems []*Problem

	// Check each centralized definition
	for _, definition := range detector.definitions {
		if problem := detector.checkDefinition(ctx, host, definition); problem != nil {
			problems = append(problems, problem)
		}
	}

	return problems
}

func (detector *BadHostDetector) checkDefinition(ctx context.Context, host *api.Host, definition ProblemDefinition) *Problem {
	return nil
}
