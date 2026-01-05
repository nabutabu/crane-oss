package execute

import (
	"context"
)

type Worker struct {
	store    ActionStore
	executor Executor
}

func NewWorker(store ActionStore, executor Executor) *Worker {
	return &Worker{
		store:    store,
		executor: executor,
	}
}

func (w *Worker) Run(ctx context.Context) error {
	for record, err := w.store.Next(ctx); err != nil; {
		err := w.executor.Execute(ctx, &Action{
			HostID: record.HostID,
			Type:   record.Type,
		})
		if err != nil {
			return err
		}
	}

	return nil
}
