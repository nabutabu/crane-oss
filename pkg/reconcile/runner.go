package reconcile

import (
	"context"
	"log"
	"time"
)

type Runner struct {
	reconciler HostReconciler
	interval   time.Duration
}

func (r *Runner) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.reconciler.Reconcile(ctx); err != nil {
				log.Printf("reconcile error: %v", err)
			}
		}
	}
}
