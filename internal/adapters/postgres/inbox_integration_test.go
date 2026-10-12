//go:build integration

// InboxRepository integration against real PostgreSQL: first completion
// inserts, same-identity redelivery replays without writing, and a rewritten
// body under the same identity is a permanent conflict.
package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/ports"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/testsupport"
)

func TestTryCompleteInboxIntegration(t *testing.T) {
	pool := testsupport.PostgresPoolOrSkip(t)
	uow := NewUnitOfWork(pool)
	inbox := NewInboxRepository()
	ctx := context.Background()

	t.Run("first completion inserts", func(t *testing.T) {
		completion := ports.InboxCompletion{
			ConsumerName: "test-consumer", MessageID: uuid.NewString(),
			PayloadHash: uuid.NewString() + "-hash", TransactionID: uuid.Nil,
			ReceivedAt: time.Now().UTC(), CompletedAt: time.Now().UTC(),
		}
		var replayed bool
		err := uow.WithinTx(ctx, func(ctx context.Context) error {
			var err error
			replayed, err = inbox.TryCompleteInbox(ctx, completion)
			return err
		})
		if err != nil || replayed {
			t.Fatalf("replayed=%v err=%v", replayed, err)
		}
		var completed *time.Time
		if err := pool.QueryRow(ctx,
			`SELECT completed_at FROM inbox_messages WHERE consumer_name=$1 AND message_id=$2`,
			completion.ConsumerName, completion.MessageID).Scan(&completed); err != nil || completed == nil {
			t.Fatalf("row missing or incomplete: %v", err)
		}
	})

	t.Run("redelivery with same hash replays without writing", func(t *testing.T) {
		completion := ports.InboxCompletion{
			ConsumerName: "test-consumer", MessageID: uuid.NewString(),
			PayloadHash: "stable-hash", TransactionID: uuid.Nil,
			ReceivedAt: time.Now().UTC(), CompletedAt: time.Now().UTC(),
		}
		if err := uow.WithinTx(ctx, func(ctx context.Context) error {
			_, err := inbox.TryCompleteInbox(ctx, completion)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		var replayed bool
		err := uow.WithinTx(ctx, func(ctx context.Context) error {
			var err error
			// A later redelivery carries the same hash and a new read time.
			second := completion
			second.ReceivedAt = second.ReceivedAt.Add(time.Minute)
			second.CompletedAt = second.CompletedAt.Add(time.Minute)
			replayed, err = inbox.TryCompleteInbox(ctx, second)
			return err
		})
		if err != nil || !replayed {
			t.Fatalf("replayed=%v err=%v", replayed, err)
		}
		var count int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM inbox_messages WHERE message_id=$1`, completion.MessageID).Scan(&count); err != nil || count != 1 {
			t.Fatalf("rows=%d err=%v", count, err)
		}
	})

	t.Run("redelivery with different hash is a permanent conflict", func(t *testing.T) {
		completion := ports.InboxCompletion{
			ConsumerName: "test-consumer", MessageID: uuid.NewString(),
			PayloadHash: "first-hash", TransactionID: uuid.Nil,
			ReceivedAt: time.Now().UTC(), CompletedAt: time.Now().UTC(),
		}
		if err := uow.WithinTx(ctx, func(ctx context.Context) error {
			_, err := inbox.TryCompleteInbox(ctx, completion)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		rewritten := completion
		rewritten.PayloadHash = "rewritten-hash"
		err := uow.WithinTx(ctx, func(ctx context.Context) error {
			_, err := inbox.TryCompleteInbox(ctx, rewritten)
			return err
		})
		if !errors.Is(err, ports.ErrInboxConflict) {
			t.Fatalf("err = %v, want ErrInboxConflict", err)
		}
	})
}
