package execute

import (
	"context"
	"testing"
	"time"
)

func TestWorkerStopsWithoutWaitingForTick(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { NewWorker(nil, nil).Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop after cancellation")
	}
}
