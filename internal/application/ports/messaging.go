package ports

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	// ErrInboxConflict: the (consumer, messageId) identity already exists with
	// a different payload hash — a redelivery rewrote the message body.
	// Permanent: the message goes to the dead-letter queue for inspection.
	ErrInboxConflict = errors.New("inbox message redelivered with different content")
)

// InboxCompletion records the durable conclusion of one consumed message in
// the same SQL transaction as the domain effects it produced.
type InboxCompletion struct {
	ConsumerName  string
	MessageID     string
	PayloadHash   string
	TransactionID uuid.UUID
	ReceivedAt    time.Time
	CompletedAt   time.Time
}

// InboxRepository deduplicates consumed messages. TryCompleteInbox inserts
// the completion; when the identity already exists with the same hash it
// reports replayed=true (the durable handling already committed) without
// writing anything, and with a different hash it returns ErrInboxConflict.
// It must run inside the caller's unit of work.
type InboxRepository interface {
	TryCompleteInbox(ctx context.Context, completion InboxCompletion) (replayed bool, err error)
}
