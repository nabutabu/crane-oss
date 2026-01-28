package reconcile

import (
	"context"
	"errors"

	"github.com/nabutabu/crane-oss/internal/creditmanager/poolstore"
	"github.com/nabutabu/crane-oss/internal/execute"
	"github.com/nabutabu/crane-oss/internal/hostcatalog/store"
	"github.com/nabutabu/crane-oss/pkg/api"
)

type AssignmentReconciler interface {
	Reconcile(ctx context.Context) error
}

type DefaultAssignmentReconciler struct {
	hStore      store.PostgresHostStore
	pStore      poolstore.PoolStore
	actionStore execute.ActionStore
}

func NewDefaultAssignmentReconciler(hStore store.PostgresHostStore, pStore poolstore.PoolStore,
	actionStore execute.ActionStore) *DefaultAssignmentReconciler {
	return &DefaultAssignmentReconciler{
		hStore:      hStore,
		pStore:      pStore,
		actionStore: actionStore,
	}
}

func (reconciler *DefaultAssignmentReconciler) findPool(ctx context.Context, host *api.Host) (*api.Pool, error) {
	pools, err := reconciler.pStore.ListPools(ctx)
	if err != nil {
		return nil, err
	}

	for _, pool := range pools {
		if pool.Role == host.Role {
			// we found a Pool?
			return pool, nil
		}
	}

	return nil, errors.New("could not find any pool that needs this host")
}

func (reconciler *DefaultAssignmentReconciler) Reconcile(ctx context.Context) error {
	hosts, err := reconciler.hStore.ListHosts(ctx)
	if err != nil {
		return err
	}

	for _, host := range hosts {
		if host.AssignedPool == nil {
			// host is unassigned, find a pool that may need it
			pool, err := reconciler.findPool(ctx, host)
			if err != nil && err.Error() != "could not find any pool that needs this host" {
				return err
			}

			err = reconciler.actionStore.Enqueue(ctx, &execute.Action{
				HostID: host.ID,
				Type:   execute.ActionAssignHost,
				PoolID: pool.ID,
			})
			if err != nil {
				return err
			}
		}
	}

	return nil
}
