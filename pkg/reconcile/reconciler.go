package reconcile

import (
	"context"
	"log"

	"github.com/nabutabu/crane-oss/internal/execute"
	"github.com/nabutabu/crane-oss/internal/hostcatalog/store"
)

type HostReconciler interface {
	Reconcile(ctx context.Context) error
}

type DefaultHostReconciler struct {
	store   store.PostgresHostStore
	execute execute.ActionStore
}

func NewDefaultHostReconciler(store store.PostgresHostStore, execute execute.ActionStore) *DefaultHostReconciler {
	return &DefaultHostReconciler{
		store:   store,
		execute: execute,
	}
}

func (r *DefaultHostReconciler) Reconcile(ctx context.Context) error {
	hosts, err := r.store.ListHosts(ctx)
	if err != nil {
		return err
	}

	for _, host := range hosts {
		action := Decide(host)
		log.Printf("For host: %s, decision: %s", host, action)
		err := r.execute.Enqueue(ctx, action)

		log.Printf("reconciling host=%s desired=%s actual=%s", host.ID, host.State, host.Health)
		if err != nil {
			return err
		}
	}

	return nil
}
