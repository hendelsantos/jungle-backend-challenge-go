package ports

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wagering"
)

var (
	// ErrWagerTransactionNotFound: no external operation matches the lookup.
	ErrWagerTransactionNotFound = errors.New("wager transaction not found")
	// ErrIdempotencyConflict: same key with different content, or the same
	// external operation under another key; the future HTTP layer answers 409.
	ErrIdempotencyConflict = errors.New("idempotency conflict for the provider operation")
)

// ResolvedReference carries the referenced transaction and whether it already
// has a successful direct compensation (REFUND or ROLLBACK), read from the
// same transaction that processes the caller.
type ResolvedReference struct {
	State       wagering.State
	Compensated bool
}

// WagerTransactionReader resolves external operations inside the unit of work
// of the caller. Lookups by key, by external identity and by reference all
// scope to the provider.
type WagerTransactionReader interface {
	FindByIdempotencyKey(ctx context.Context, providerID, idempotencyKey string) (wagering.State, error)
	FindByExternalID(ctx context.Context, providerID, externalTransactionID string) (wagering.State, error)
	FindReference(ctx context.Context, providerID, referenceExternalTransactionID string) (ResolvedReference, error)
}

// WagerQueryReader requires the authorized provider in both lookup forms.
// A UUID lookup without provider scope is deliberately not exposed.
type WagerQueryReader interface {
	FindByID(ctx context.Context, providerID string, id uuid.UUID) (wagering.State, error)
	FindByExternalID(ctx context.Context, providerID, externalID string) (wagering.State, error)
}

// ReferenceSchedule is the durable retry plan of a pending reference. The zero
// value means "not scheduled yet": a freshly persisted PENDING_REFERENCE has
// NULL scheduling columns and stays claimable until the worker plans it.
type ReferenceSchedule struct {
	Attempts       int
	NextAttemptAt  time.Time
	HasNextAttempt bool
	DeadlineAt     time.Time
	HasDeadline    bool
}

// DuePendingReference is one claimed pending reference with its schedule.
// The claim locks the row FOR UPDATE SKIP LOCKED inside the caller's unit of
// work, so concurrent workers never process the same pending operation.
type DuePendingReference struct {
	State wagering.State
	ReferenceSchedule
}

// PendingReferenceClaimer hands due pending references to the retry command.
// The claim must run inside the caller's transaction.
type PendingReferenceClaimer interface {
	ClaimDuePendingReferences(ctx context.Context, now time.Time, limit int) ([]DuePendingReference, error)
}
