package workers

import (
	"context"
	"log/slog"
	"time"
)

// OutboxWorker drains up to 50 independent claims per tick. Cancellation stops
// claims and aborts network I/O; the command releases known leases on failure.
type OutboxWorker struct {
	PublishNext func(context.Context) (bool, error)
	Interval    time.Duration
	Logger      *slog.Logger
}

func (w *OutboxWorker) Run(ctx context.Context) {
	interval := w.Interval
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for n := 0; n < 50 && ctx.Err() == nil; n++ {
				found, err := w.PublishNext(ctx)
				if err != nil {
					if w.Logger != nil && ctx.Err() == nil {
						w.Logger.Error("outbox cycle failed; durable retry remains available")
					}
					break
				}
				if !found {
					break
				}
			}
		}
	}
}
