package execute

import (
	"context"
)

type Worker struct {
	store    ActionStore
	executor Executor
}

func (w *Worker) do(ctx context.Context) error {
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
