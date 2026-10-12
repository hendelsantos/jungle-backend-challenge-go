// Package workers hosts long-running background loops. The package never
// imports Fx; bootstrap attaches lifecycle hooks to these workers.
package workers

import (
	"context"
	"log/slog"
	"time"
)

// ReferenceWorker resumes due PENDING_REFERENCE operations on a fixed
// interval. Any number of instances may run concurrently: the claim uses
// FOR UPDATE SKIP LOCKED, so rows are never processed twice and workers
// never block each other.
type ReferenceWorker struct {
	// Retry resumes up to limit due pending references; it is the command's
	// method value, injected as a function so the loop needs no more than
	// its single entry point.
	Retry      func(ctx context.Context, limit int) (int, error)
	Interval   time.Duration
	BatchLimit int
	Logger     *slog.Logger
}

// Run polls until the context is cancelled. Cancellation stops new polls
// immediately; a batch already in flight either finishes and commits, or the
// context aborts it and the transaction rolls back, leaving every claimed
// pending reference due again for any instance. Run returns when both the
// loop and the in-flight batch have ended.
func (w *ReferenceWorker) Run(ctx context.Context) {
	interval := w.Interval
	if interval <= 0 {
		interval = time.Second
	}
	limit := w.BatchLimit
	if limit < 1 {
		limit = 50
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			processed, err := w.Retry(ctx, limit)
			switch {
			case err != nil && w.Logger != nil:
				// Transient: the rows stay due and the next tick retries.
				w.Logger.Error("reference worker batch failed", "error", err, "processed", processed)
			case processed > 0 && w.Logger != nil:
				w.Logger.Info("reference worker resumed pending operations", "count", processed)
			}
		}
	}
}
