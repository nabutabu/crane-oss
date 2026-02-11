package reconcile

import (
	"context"
)

type HostReconciler interface {
	Reconcile(ctx context.Context) error
}

type DefaultHostReconciler struct {
}
