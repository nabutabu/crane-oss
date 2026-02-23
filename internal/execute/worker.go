package execute

import (
	"context"
	"log"
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

	for range ticker.C {
		record, err := w.store.Next(ctx)
		if err != nil {
			log.Printf("failed to fetch next record: %v", err)
			return
		}

		log.Println("calling execute")
		err = w.executor.Execute(ctx, &Action{
			HostID: record.HostID,
			Type:   record.Type,
		})
		if err != nil {
			// action failed
			w.store.MarkFailed(ctx, record.ID)
			log.Println(err)
		}

		// mark action completed
		err = w.store.MarkDone(ctx, record.ID)
		if err != nil {
			log.Println(err)
		}
	}
}
