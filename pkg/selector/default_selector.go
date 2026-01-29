package selector

import (
	"context"
	"errors"

	"github.com/nabutabu/crane-oss/internal/creditmanager"
	"github.com/nabutabu/crane-oss/internal/creditmanager/poolstore"
	"github.com/nabutabu/crane-oss/pkg/api"
)

type DefaultPoolSelector struct {
	pStore  poolstore.PoolStore
	Manager creditmanager.Manager
}

func (s *DefaultPoolSelector) SelectPool(ctx context.Context, host *api.Host) (*api.Pool, error) {
	pools, err := s.pStore.ListPools(ctx)
	if err != nil {
		return nil, err
	}

	for _, pool := range pools {
		if pool.Role == host.Role {
			// we found a Pool
			ok, err := s.Manager.CanReserve(pool.ID, 1)
			if err != nil {
				continue
			}
			if ok {
				return pool, nil
			}
		}
	}

	return nil, errors.New("No pool found")
}

func (s *DefaultPoolSelector) Reserve(poolID string, amount int) error {
	return s.Manager.Reserve(poolID, amount)
}

func (s *DefaultPoolSelector) Release(poolID string, amount int) error {
	return s.Manager.Release(poolID, amount)
}
