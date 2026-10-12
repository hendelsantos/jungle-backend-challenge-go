package workers

import (
	"context"
	"testing"
	"time"
)

func TestOutboxWorkerStopsActivePublication(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	done := make(chan struct{})
	worker := OutboxWorker{Interval: time.Millisecond, PublishNext: func(ctx context.Context) (bool, error) {
		close(entered)
		<-ctx.Done()
		return true, ctx.Err()
	}}
	go func() { defer close(done); worker.Run(ctx) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("worker never started")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker leaked after cancellation")
	}
}
