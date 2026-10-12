package ports

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

var ErrOutboxLeaseLost = errors.New("outbox lease expired or replaced")

// OutboxDelivery is a committed snapshot, never a newly generated envelope.
// LeaseToken identifies this claim, not a process: stale workers cannot ack a
// later claim, even if the same process eventually acquires it again.
type OutboxDelivery struct {
	EventID     uuid.UUID
	AggregateID uuid.UUID
	Payload     []byte
	Attempts    int
	LeaseToken  string
}

type OutboxStats struct {
	Pending          int64
	OldestAgeSeconds int64
}

// Delivery operations each commit their own short SQL statement. The broker
// call occurs between them, with no database transaction or row lock held.
type OutboxDeliveryRepository interface {
	ClaimNext(ctx context.Context, token string, lease time.Duration) (OutboxDelivery, bool, error)
	MarkPublished(ctx context.Context, eventID uuid.UUID, token string) error
	ScheduleRetry(ctx context.Context, eventID uuid.UUID, token string, delay time.Duration, code string) error
	Stats(ctx context.Context) (OutboxStats, error)
}

type EventPublisher interface {
	Publish(ctx context.Context, event OutboxDelivery) error
}
