package workers_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hendelsantos/jungle-backend-challenge-go/internal/workers"
)

func TestReferenceWorkerRunsUntilCancelled(t *testing.T) {
	var calls, limits atomic.Int64
	release := make(chan struct{})
	worker := &workers.ReferenceWorker{
		Retry: func(ctx context.Context, limit int) (int, error) {
			if calls.Add(1) == 1 {
				close(release)
			}
			limits.Store(int64(limit))
			return 1, nil
		},
		Interval:   time.Millisecond,
		BatchLimit: 7,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.Run(ctx)
	}()
	<-release
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop after cancellation")
	}
	if calls.Load() < 1 {
		t.Fatalf("polls = %d", calls.Load())
	}
	if limits.Load() != 7 {
		t.Fatalf("batch limit forwarded = %d", limits.Load())
	}
}

func TestReferenceWorkerDefaultsIntervalAndBatch(t *testing.T) {
	var calls atomic.Int64
	first := make(chan struct{})
	worker := &workers.ReferenceWorker{
		Retry: func(context.Context, int) (int, error) {
			if calls.Add(1) == 1 {
				close(first)
			}
			return 0, nil
		},
		// Zero interval and batch fall back to 1s/50; the test only proves the
		// fallbacks keep the loop alive, not their exact values.
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.Run(ctx)
	}()
	select {
	case <-first:
		t.Fatal("zero interval must default to a real interval, not a busy loop")
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop after cancellation")
	}
}

func TestReferenceWorkerSurvivesBatchErrors(t *testing.T) {
	var calls atomic.Int64
	fail := make(chan struct{})
	worker := &workers.ReferenceWorker{
		Retry: func(context.Context, int) (int, error) {
			if calls.Add(1) == 2 {
				close(fail)
			}
			if calls.Load() == 1 {
				return 0, errors.New("transient database failure")
			}
			return 0, nil
		},
		Interval: time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.Run(ctx)
	}()
	<-fail
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not survive a failed batch")
	}
	if calls.Load() < 2 {
		t.Fatalf("worker stopped polling after the first failure: %d", calls.Load())
	}
}
