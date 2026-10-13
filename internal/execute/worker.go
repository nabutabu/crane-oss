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

	slog.Info("executor worker starting")

	for range ticker.C {
		record, err := w.store.Next(ctx)
		if err != nil {
			slog.Error("failed to fetch next action", "error", err)
			continue
		}

		slog.Debug("calling execute", "action_id", record.ID, "host_id", record.HostID, "action", record.Type)
		err = w.executor.Execute(ctx, &Action{
			HostID: record.HostID,
			Type:   record.Type,
		})
		if err != nil {
			// action failed
			w.store.MarkFailed(ctx, record.ID)
			slog.Error("action failed", "action_id", record.ID, "host_id", record.HostID, "error", err)
		}

		// mark action completed
		err = w.store.MarkDone(ctx, record.ID)
		if err != nil {
			slog.Error("failed to mark action done", "action_id", record.ID, "error", err)
		}
	}
}
