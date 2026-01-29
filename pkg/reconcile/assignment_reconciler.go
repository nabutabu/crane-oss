package reconcile

import (
	"context"
	"log"

	"github.com/nabutabu/crane-oss/internal/execute"
	"github.com/nabutabu/crane-oss/internal/hostcatalog/store"
	"github.com/nabutabu/crane-oss/pkg/selector"
)

type AssignmentReconciler interface {
	Reconcile(ctx context.Context) error
}

type DefaultAssignmentReconciler struct {
	HStore      store.PostgresHostStore
	Selector    selector.PoolSelector
	ActionStore execute.ActionStore
}

func NewDefaultAssignmentReconciler(hStore store.PostgresHostStore, poolselector selector.PoolSelector,
	actionStore execute.ActionStore) *DefaultAssignmentReconciler {
	return &DefaultAssignmentReconciler{
		HStore:      hStore,
		Selector:    poolselector,
		ActionStore: actionStore,
	}
}

func (reconciler *DefaultAssignmentReconciler) Reconcile(ctx context.Context) error {
	hosts, err := reconciler.HStore.ListHosts(ctx)
	if err != nil {
		return err
	}

	for _, host := range hosts {
		if host.AssignedPool == nil {
			// host is unassigned, find a pool that may need it
			pool, err := reconciler.Selector.SelectPool(ctx, host)
			if err != nil && err.Error() != "could not find any pool that needs this host" {
				return err
			}

			// reserve credits
			err = reconciler.Selector.Reserve(pool.ID, 1) // TODO: amount hardcoded rn
			if err != nil {
				return err
			}

			// TODO: lots of hardcodedness for credits

			err = reconciler.ActionStore.Enqueue(ctx, &execute.Action{
				HostID: host.ID,
				Type:   execute.ActionAssignHost,
				PoolID: pool.ID,
				Cost:   1,
			})
			if err != nil {
				log.Printf("Error while enqueue action from AssignmentReconciler: %s", err)
				// return the credits if enqueue failed
				err = reconciler.Selector.Release(pool.ID, 1) // TODO
				if err != nil {
					return err
				}

				return err
			}
		}
	}

	return nil
}
