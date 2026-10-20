package badhost

import (
	"context"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
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

	slog.InfoContext(ctx, "bad host detector starting", "operation", "badhost.scan",
		"zone", detector.cfg.Zone,
		"scan_interval", detector.cfg.ScanInterval,
		"checks", len(detector.checks),
	)

	for {
		select {
		case <-ctx.Done():
			slog.InfoContext(ctx, "bad host detector shutting down", "operation", "badhost.scan")
			return

		case <-ticker.C:
			detector.ScanZone(ctx, detector.cfg.Zone)
		}
	}
}

func (detector *BadHostDetector) ScanZone(ctx context.Context, zone string) {
	ctx, span := otel.Tracer("crane.background").Start(ctx, "badhost.scan", trace.WithAttributes(attribute.String("zone", zone)))
	defer func() { span.End() }()
	// 1. Get all hosts in zone from host catalog
	hosts, err := detector.hostStore.GetByZone(ctx, zone)
	if err != nil {
		slog.ErrorContext(ctx, "failed to list hosts in zone", "operation", "badhost.scan", "zone", zone, "error", err)
	}

	// 2. Scan each host for problems
	for _, host := range hosts {
		problems := detector.detectProblems(ctx, host)
		if len(problems) > 0 {
			// 3. Record problems in central database
			slog.InfoContext(ctx, "recording problems", "operation", "badhost.scan", "host_id", host.ID, "count", len(problems))
			for _, problem := range problems {
				err = detector.problemStore.RecordProblem(ctx, host.ID, *problem)
				if err != nil {
					slog.ErrorContext(ctx, "failed to record problem", "operation", "badhost.scan", "host_id", host.ID, "error", err)
				}
			}
		}
	}

}

func (detector *BadHostDetector) detectProblems(ctx context.Context, host *api.Host) []*problem.Problem {
	var problems []*problem.Problem

	for _, check := range detector.checks {
		slog.DebugContext(ctx, "running check", "operation", "badhost.scan", "check", check.Name(), "host_id", host.ID)
		ps, err := check.Detect(ctx, host)
		if err != nil {
			slog.ErrorContext(ctx, "check failed", "operation", "badhost.scan", "check", check.Name(), "host_id", host.ID, "error", err)
			continue
		}

		for _, p := range ps {
			problems = append(problems, &p)
		}
	}

	return problems
}
