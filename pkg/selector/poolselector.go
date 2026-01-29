package selector

import (
	"context"

	"github.com/nabutabu/crane-oss/pkg/api"
)

type PoolSelector interface {
	Reserve(poolID string, amount int) error
	SelectPool(ctx context.Context, host *api.Host) (*api.Pool, error)
	Release(poolID string, amount int) error
}
