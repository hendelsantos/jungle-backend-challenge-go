package command

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/ports"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/observability"
)

const OutboxLease = 30 * time.Second
const OutboxPublishTimeout = 10 * time.Second

type PublishOutboxDeps struct {
	Outbox    ports.OutboxDeliveryRepository
	Publisher ports.EventPublisher
	Metrics   *observability.OutboxMetrics
	Logger    *slog.Logger
}

// PublishNext claims exactly one committed event. No batch is leased ahead of
// network I/O. Failure after send but before ack leaves the claim recoverable;
// the next publisher sends the original payload/eventId again (at-least-once).
func (d PublishOutboxDeps) PublishNext(parent context.Context) (bool, error) {
	if d.Outbox == nil || d.Publisher == nil || d.Metrics == nil {
		return false, ErrIncompleteDeps
	}
	ctx, cancel := context.WithTimeout(parent, OutboxPublishTimeout)
	defer cancel()
	event, found, err := d.Outbox.ClaimNext(ctx, uuid.NewString(), OutboxLease)
	if err != nil || !found {
		return false, err
	}
	d.Metrics.Claimed.Add(1)
	if event.Attempts > 1 {
		d.Metrics.Retries.Add(1)
	}
	if err := d.Publisher.Publish(ctx, event); err != nil {
		d.Metrics.PublishFailures.Add(1)
		delay, code := outboxBackoff(event.Attempts), "PUBLISH_FAILED"
		if parent.Err() != nil {
			delay, code = 0, "CANCELLED"
		}
		// Release known work even after shutdown/timeout, with a bounded cleanup.
		cleanup, stop := context.WithTimeout(context.WithoutCancel(parent), 2*time.Second)
		defer stop()
		retryErr := d.Outbox.ScheduleRetry(cleanup, event.EventID, event.LeaseToken, delay, code)
		if errors.Is(retryErr, ports.ErrOutboxLeaseLost) {
			d.Metrics.LeaseLost.Add(1)
		}
		if retryErr != nil {
			d.Metrics.ConfirmationFailures.Add(1)
		}
		d.log("outbox publish failed", event, code)
		return true, errors.Join(fmt.Errorf("publish outbox: %w", err), retryErr)
	}
	if err := d.Outbox.MarkPublished(ctx, event.EventID, event.LeaseToken); err != nil {
		d.Metrics.ConfirmationFailures.Add(1)
		if errors.Is(err, ports.ErrOutboxLeaseLost) {
			d.Metrics.LeaseLost.Add(1)
		}
		d.log("outbox confirmation failed; lease will recover", event, "CONFIRMATION_FAILED")
		return true, fmt.Errorf("confirm outbox: %w", err)
	}
	d.Metrics.Published.Add(1)
	d.log("outbox event published", event, "")
	return true, nil
}

func (d PublishOutboxDeps) log(message string, event ports.OutboxDelivery, code string) {
	if d.Logger == nil {
		return
	}
	args := []any{"eventId", event.EventID, "aggregateId", event.AggregateID, "attempt", event.Attempts}
	if code != "" {
		d.Logger.Error(message, append(args, "errorCode", code)...)
		return
	}
	d.Logger.Info(message, args...)
}

// Unbounded retries with bounded delay: committed events are never discarded.
func outboxBackoff(attempt int) time.Duration {
	if attempt <= 1 {
		return time.Second
	}
	if attempt >= 10 {
		return 5 * time.Minute
	}
	return time.Second << (attempt - 1)
}
