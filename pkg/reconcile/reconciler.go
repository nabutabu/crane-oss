package reconcile

import (
	"context"
	"github.com/nabutabu/crane-oss/internal/execute"
	"github.com/nabutabu/crane-oss/internal/hostcatalog/store"
	"log"
)

type HostReconciler interface {
	Reconcile(ctx context.Context) error
}

type DefaultHostReconciler struct {
	store   store.PostgresHostStore
	execute execute.Executor
}

func (r *DefaultHostReconciler) Reconcile(ctx context.Context) error {
	hosts, err := r.store.ListHosts(ctx)
	if err != nil {
		return err
	}

	for _, host := range hosts {
		action := Decide(host)
		log.Printf("For host: %s, decision: %s", host, action)
		err := r.execute.Execute(ctx, action)
		if err != nil {
			return err
		}
	}

	return nil
}
