package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/ports"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var _ ports.OutboxDeliveryRepository = (*OutboxDeliveryRepository)(nil)

type OutboxDeliveryRepository struct{ pool *pgxpool.Pool }

func NewOutboxDeliveryRepository(pool *pgxpool.Pool) *OutboxDeliveryRepository {
	return &OutboxDeliveryRepository{pool: pool}
}

func (r *OutboxDeliveryRepository) ClaimNext(ctx context.Context, token string, lease time.Duration) (ports.OutboxDelivery, bool, error) {
	if token == "" || lease < time.Millisecond {
		return ports.OutboxDelivery{}, false, fmt.Errorf("outbox: invalid lease")
	}
	var event ports.OutboxDelivery
	// Read committed excludes financial transactions that have not committed.
	// SKIP LOCKED lets other instances claim independent rows without waiting.
	// Database time avoids clock skew between hosts; attempts saturate at SQL int max.
	err := r.pool.QueryRow(ctx, `WITH candidate AS (
  SELECT event_id FROM outbox_events
  WHERE published_at IS NULL AND next_attempt_at <= clock_timestamp()
    AND (leased_until IS NULL OR leased_until <= clock_timestamp())
  ORDER BY next_attempt_at, event_id FOR UPDATE SKIP LOCKED LIMIT 1
 ) UPDATE outbox_events AS o SET leased_by=$1,
   leased_until=clock_timestamp() + $2 * interval '1 millisecond',
   attempts=LEAST(o.attempts::bigint+1,2147483647)::integer
 FROM candidate c WHERE o.event_id=c.event_id
 RETURNING o.event_id,o.aggregate_id,o.payload_json,o.attempts,o.leased_by`, token, lease.Milliseconds()).
		Scan(&event.EventID, &event.AggregateID, &event.Payload, &event.Attempts, &event.LeaseToken)
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.OutboxDelivery{}, false, nil
	}
	if err != nil {
		return ports.OutboxDelivery{}, false, fmt.Errorf("postgres: claim outbox: %w", err)
	}
	return event, true, nil
}

func (r *OutboxDeliveryRepository) MarkPublished(ctx context.Context, eventID uuid.UUID, token string) error {
	tag, err := r.pool.Exec(ctx, `UPDATE outbox_events SET published_at=clock_timestamp(),
  leased_by=NULL, leased_until=NULL, last_error=NULL
  WHERE event_id=$1 AND leased_by=$2 AND published_at IS NULL AND leased_until > clock_timestamp()`, eventID, token)
	if err != nil {
		return fmt.Errorf("postgres: acknowledge outbox: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ports.ErrOutboxLeaseLost
	}
	return nil
}

func (r *OutboxDeliveryRepository) ScheduleRetry(ctx context.Context, eventID uuid.UUID, token string, delay time.Duration, code string) error {
	if delay < 0 || len(code) > 64 {
		return fmt.Errorf("outbox: invalid retry schedule")
	}
	tag, err := r.pool.Exec(ctx, `UPDATE outbox_events SET
  next_attempt_at=clock_timestamp() + $3 * interval '1 millisecond', last_error=$4,
  leased_by=NULL, leased_until=NULL
  WHERE event_id=$1 AND leased_by=$2 AND published_at IS NULL AND leased_until > clock_timestamp()`, eventID, token, delay.Milliseconds(), code)
	if err != nil {
		return fmt.Errorf("postgres: schedule outbox: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ports.ErrOutboxLeaseLost
	}
	return nil
}

func (r *OutboxDeliveryRepository) Stats(ctx context.Context) (ports.OutboxStats, error) {
	var stats ports.OutboxStats
	err := r.pool.QueryRow(ctx, `SELECT count(*),
  COALESCE(GREATEST(0,FLOOR(EXTRACT(EPOCH FROM (clock_timestamp()-min(created_at)))))::bigint,0)
  FROM outbox_events WHERE published_at IS NULL`).Scan(&stats.Pending, &stats.OldestAgeSeconds)
	return stats, err
}
