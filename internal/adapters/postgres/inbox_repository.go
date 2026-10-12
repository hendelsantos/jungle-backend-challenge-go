package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/ports"
)

var _ ports.InboxRepository = (*InboxRepository)(nil)

// InboxRepository implements the inbox deduplication port. The completion is
// written by the same transaction that persists the domain effects; the
// PRIMARY KEY (consumer_name, message_id) decides races between consumers.
type InboxRepository struct{}

func NewInboxRepository() *InboxRepository { return &InboxRepository{} }

func (InboxRepository) TryCompleteInbox(ctx context.Context, completion ports.InboxCompletion) (bool, error) {
	tx, err := activeTx(ctx, "complete inbox message")
	if err != nil {
		return false, err
	}
	// ON CONFLICT DO NOTHING keeps the transaction usable after a collision:
	// a failed INSERT would abort the whole transaction (25P02) and the
	// follow-up hash comparison could never run.
	tag, err := tx.Exec(ctx, `
		INSERT INTO inbox_messages (
			consumer_name, message_id, payload_hash, received_at, completed_at, transaction_id
		) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (consumer_name, message_id) DO NOTHING`,
		completion.ConsumerName, completion.MessageID, completion.PayloadHash,
		completion.ReceivedAt, completion.CompletedAt, nullUUID(completion.TransactionID))
	if err != nil {
		return false, fmt.Errorf("postgres: complete inbox message: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return false, nil
	}
	var existing string
	var existingTransaction uuid.UUID
	var completedAt *time.Time
	err = tx.QueryRow(ctx,
		`SELECT payload_hash, COALESCE(transaction_id, '00000000-0000-0000-0000-000000000000'::uuid), completed_at FROM inbox_messages WHERE consumer_name = $1 AND message_id = $2`,
		completion.ConsumerName, completion.MessageID).Scan(&existing, &existingTransaction, &completedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		// Lost a race inside the same statement batch: retry the whole unit.
		return false, fmt.Errorf("postgres: inbox row vanished after conflict: %w", ports.ErrRetryableTransaction)
	}
	if err != nil {
		return false, fmt.Errorf("postgres: read inbox conflict: %w", err)
	}
	if existing != completion.PayloadHash || existingTransaction != completion.TransactionID || completedAt == nil {
		return false, fmt.Errorf("postgres: consumer %s message %s: %w",
			completion.ConsumerName, completion.MessageID, ports.ErrInboxConflict)
	}
	// Same identity, same content: the durable handling already committed.
	return true, nil
}
