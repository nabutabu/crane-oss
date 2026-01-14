package badhost

import (
	"context"

	"github.com/nabutabu/crane-oss/pkg/api"
)

type Check interface {
	Name() string
	Detect(ctx context.Context, host *api.Host) ([]Problem, error)
}
