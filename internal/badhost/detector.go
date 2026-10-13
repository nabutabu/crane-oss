package badhost

import (
	"context"
	"log/slog"
	"time"

	"github.com/nabutabu/crane-oss/internal/badhost/checks"
	"github.com/nabutabu/crane-oss/internal/badhost/problem"
	"github.com/nabutabu/crane-oss/internal/hostcatalog/service"
	"github.com/nabutabu/crane-oss/pkg/api"
)

type BadHostDetector struct {
	hostStore    service.HostCatalogService
	problemStore problem.ProblemStore
	cfg          Config
	checks       []checks.Check
}

func New(hostStore *service.HostCatalogService, problemStore problem.ProblemStore, checks []checks.Check, cfg *Config) *BadHostDetector {
	return &BadHostDetector{
		hostStore:    *hostStore,
		problemStore: problemStore,
		checks:       checks,
		cfg:          *cfg,
	}
}

func (detector *BadHostDetector) Run(ctx context.Context) {
	ticker := time.NewTicker(detector.cfg.ScanInterval)
	defer ticker.Stop()

	slog.Info("bad host detector starting",
		"zone", detector.cfg.Zone,
		"scan_interval", detector.cfg.ScanInterval,
		"checks", len(detector.checks),
	)

	for {
		select {
		case <-ctx.Done():
			slog.Info("bad host detector shutting down")
			return

		case <-ticker.C:
			detector.ScanZone(ctx, detector.cfg.Zone)
		}
	}
}

func (detector *BadHostDetector) ScanZone(ctx context.Context, zone string) {
	// 1. Get all hosts in zone from host catalog
	hosts, err := detector.hostStore.GetByZone(ctx, zone)
	if err != nil {
		slog.Error("failed to list hosts in zone", "zone", zone, "error", err)
	}

	// 2. Scan each host for problems
	for _, host := range hosts {
		problems := detector.detectProblems(ctx, host)
		if len(problems) > 0 {
			// 3. Record problems in central database
			slog.Info("recording problems", "host_id", host.ID, "count", len(problems))
			for _, problem := range problems {
				err = detector.problemStore.RecordProblem(ctx, host.ID, *problem)
				if err != nil {
					slog.Error("failed to record problem", "host_id", host.ID, "error", err)
				}
			}
		}
	}

}

func (detector *BadHostDetector) detectProblems(ctx context.Context, host *api.Host) []*problem.Problem {
	var problems []*problem.Problem

	for _, check := range detector.checks {
		slog.Debug("running check", "check", check.Name(), "host_id", host.ID)
		ps, err := check.Detect(ctx, host)
		if err != nil {
			slog.Error("check failed", "check", check.Name(), "host_id", host.ID, "error", err)
			continue
		}

		for _, p := range ps {
			problems = append(problems, &p)
		}
	}

	return problems
}
