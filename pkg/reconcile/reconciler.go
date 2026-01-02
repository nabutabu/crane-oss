package reconcile

import (
	"context"
	"log"

	"github.com/nabutabu/crane-oss/internal/hostcatalog/store"
)

type HostReconciler interface {
	Reconcile(ctx context.Context) error
}

type DefaultHostReconciler struct {
	store store.PostgresHostStore
}

func (r *DefaultHostReconciler) Reconcile(ctx context.Context) error {
	hosts, err := r.store.ListHosts(ctx)
	if err != nil {
		return err
	}

	for _, host := range hosts {
		decision := Decide(host)
		log.Printf("For host: %s, decision: %s", host, decision)
	}

	return nil
}
