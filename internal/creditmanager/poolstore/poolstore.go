package poolstore

import (
	"context"

	"github.com/nabutabu/crane-oss/pkg/api"
)

type PoolStore interface {
	ListPools(ctx context.Context) ([]*api.Pool, error)
	GetPool(ctx context.Context, id string) (*api.Pool, error)
}
