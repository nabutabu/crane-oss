package execute

import (
	"context"
	"log"
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
	for record, err := w.store.Next(ctx); err == nil; {
		log.Printf("failed to fetch next record: %v", err)

		log.Println("calling execute")
		err := w.executor.Execute(ctx, &Action{
			HostID: record.HostID,
			Type:   record.Type,
		})
		if err != nil {
			// action failed
			w.store.MarkFailed(ctx, record.ID)
			log.Println(err)
			return err
		}

		// mark action completed
		err = w.store.MarkDone(ctx, record.ID)
		if err != nil {
			log.Println(err)
			return err
		}
	}

	log.Println("returning nil")
	return nil
}
