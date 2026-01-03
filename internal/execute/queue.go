package execute

import "context"

type ActionStore interface {
	Enqueue(ctx context.Context, action *Action) error
	Next(ctx context.Context) (*ActionRecord, error)
	MarkDone(ctx context.Context, id int) error
	MarkFailed(ctx context.Context, id int) error
}
