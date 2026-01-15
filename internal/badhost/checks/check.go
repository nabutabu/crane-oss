package checks

import (
	"context"

	"github.com/nabutabu/crane-oss/pkg/api"
	"github.com/nabutabu/crane-oss/internal/badhost/problem"
)

type Check interface {
	Name() string
	Detect(ctx context.Context, host *api.Host) ([]problem.Problem, error)
}
