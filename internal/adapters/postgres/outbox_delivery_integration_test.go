//go:build integration

// Outbox delivery semantics against real PostgreSQL: a claim is exclusive and
// attempts increment, a stale lease can neither acknowledge nor reschedule,
// and an abandoned lease is recoverable by the next claim (the database half
// of a crash between publish and acknowledgement).
package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/ports"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/testsupport"
)

// insertCommittedEvent writes a row exactly as a committed financial
// transaction would leave behind: pending (published_at IS NULL) and eligible
// for immediate claiming.
func insertCommittedEvent(t *testing.T, pool *pgxpool.Pool) (uuid.UUID, uuid.UUID) {
	t.Helper()
	eventID, aggregateID := uuid.New(), uuid.New()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx, `INSERT INTO outbox_events (
		event_id, aggregate_id, event_type, payload_json, occurred_at, next_attempt_at, created_at
	) VALUES ($1,$2,'WalletBalanceChanged',$3,$4,$4,$4)`,
		eventID, aggregateID, []byte(`{"eventId":"`+eventID.String()+`"}`), time.Now().UTC()); err != nil {
		t.Fatalf("seed outbox event: %v", err)
	}
	return eventID, aggregateID
}

func TestOutboxDeliveryClaimAndAcknowledgeIntegration(t *testing.T) {
	pool := testsupport.IsolatedPostgresPool(t)
	store := NewOutboxDeliveryRepository(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventID, _ := insertCommittedEvent(t, pool)

	claimed, found, err := store.ClaimNext(ctx, "publisher-1", 30*time.Second)
	if err != nil || !found {
		t.Fatalf("claim: found=%v err=%v", found, err)
	}
	if claimed.EventID != eventID {
		t.Fatalf("claimed %v, want %v", claimed.EventID, eventID)
	}
	if claimed.Attempts != 1 || claimed.LeaseToken != "publisher-1" {
		t.Fatalf("attempts=%d token=%q", claimed.Attempts, claimed.LeaseToken)
	}

	if _, found, _ := store.ClaimNext(ctx, "publisher-2", 30*time.Second); found {
		t.Fatal("second claim succeeded while the event is leased")
	}

	if err := store.MarkPublished(ctx, eventID, "publisher-2"); !errors.Is(err, ports.ErrOutboxLeaseLost) {
		t.Fatalf("stale ack: %v", err)
	}

	stats, err := store.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Pending != 1 {
		t.Fatalf("pending=%d before ack", stats.Pending)
	}

	if err := store.MarkPublished(ctx, eventID, "publisher-1"); err != nil {
		t.Fatalf("ack with owning token: %v", err)
	}
	stats, err = store.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Pending != 0 || stats.OldestAgeSeconds != 0 {
		t.Fatalf("stats after ack: %+v", stats)
	}
	if _, found, _ := store.ClaimNext(ctx, "publisher-2", 30*time.Second); found {
		t.Fatal("claimed an event that is already published")
	}
}

func TestOutboxDeliveryAbandonedLeaseAndRetryIntegration(t *testing.T) {
	pool := testsupport.IsolatedPostgresPool(t)
	store := NewOutboxDeliveryRepository(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventID, _ := insertCommittedEvent(t, pool)

	// A generous lease makes the "live lease blocks other claims" assertion
	// immune to scheduling delays on loaded machines; the expiry below is
	// then forced deterministically instead of relying on wall-clock sleep.
	if _, found, err := store.ClaimNext(ctx, "crashed-publisher", 30*time.Second); err != nil || !found {
		t.Fatalf("claim: found=%v err=%v", found, err)
	}
	if _, found, err := store.ClaimNext(ctx, "other", time.Second); err != nil {
		t.Fatalf("check live lease: %v", err)
	} else if found {
		t.Fatal("claimed an event with a live lease")
	}

	// The first publisher dies without publishing or acknowledging. Force the
	// lease to expire (the claim predicate leased_until <= clock_timestamp()
	// is exercised identically, without timing flakiness), then the next
	// claim must recover the same committed event.
	if _, err := pool.Exec(ctx, `UPDATE outbox_events SET leased_until = clock_timestamp() - interval '1 millisecond' WHERE event_id = $1`, eventID); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
	recovered, found, err := store.ClaimNext(ctx, "recovery-publisher", 30*time.Second)
	if err != nil || !found {
		t.Fatalf("recover claim: found=%v err=%v", found, err)
	}
	if recovered.EventID != eventID || recovered.Attempts != 2 {
		t.Fatalf("recovered event=%v attempts=%d", recovered.EventID, recovered.Attempts)
	}

	if err := store.ScheduleRetry(ctx, eventID, "crashed-publisher", time.Second, "PUBLISH_FAILED"); !errors.Is(err, ports.ErrOutboxLeaseLost) {
		t.Fatalf("stale reschedule: %v", err)
	}
	if err := store.ScheduleRetry(ctx, eventID, "recovery-publisher", time.Second, "PUBLISH_FAILED"); err != nil {
		t.Fatalf("reschedule: %v", err)
	}

	row := pool.QueryRow(ctx, `SELECT leased_by IS NULL, last_error FROM outbox_events WHERE event_id=$1`, eventID)
	var released bool
	var lastError *string
	if err := row.Scan(&released, &lastError); err != nil {
		t.Fatalf("inspect rescheduled event: %v", err)
	}
	if !released || lastError == nil || *lastError != "PUBLISH_FAILED" {
		t.Fatalf("released=%v lastError=%v", released, lastError)
	}

	if _, found, err := store.ClaimNext(ctx, "other", time.Second); err != nil {
		t.Fatalf("check retry backoff: %v", err)
	} else if found {
		t.Fatal("claimed an event whose retry backoff has not elapsed")
	}
}
