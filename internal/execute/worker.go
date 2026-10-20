package execute

import (
	"context"
	"log/slog"
	"time"
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

func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Minute * 1)
	defer ticker.Stop()

	slog.InfoContext(ctx, "executor worker starting", "operation", "action.worker")

	for {
		select {
		case <-ctx.Done():
			slog.InfoContext(ctx, "executor worker shutting down", "operation", "action.worker")
			return
		case <-ticker.C:
		}
		record, err := w.store.Next(ctx)
		if err != nil {
			slog.ErrorContext(ctx, "failed to fetch next action", "operation", "action.worker", "error", err)
			continue
		}

		slog.DebugContext(ctx, "calling execute", "operation", "action.worker", "action_id", record.ID, "host_id", record.HostID, "action", record.Type)
		err = w.executor.Execute(ctx, &Action{
			HostID: record.HostID,
			Type:   record.Type,
		})
		if err != nil {
			// action failed
			w.store.MarkFailed(ctx, record.ID)
			slog.ErrorContext(ctx, "action failed", "operation", "action.worker", "action_id", record.ID, "host_id", record.HostID, "error", err)
		}

		// mark action completed
		err = w.store.MarkDone(ctx, record.ID)
		if err != nil {
			slog.ErrorContext(ctx, "failed to mark action done", "operation", "action.worker", "action_id", record.ID, "error", err)
		}
	}
}
