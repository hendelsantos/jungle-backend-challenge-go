package ports

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/events"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/ledger"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wagering"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wallet"
)

var (
	// ErrWalletAlreadyExists marks the UNIQUE(player_id, currency) conflict; HTTP
	// will translate it to 409.
	ErrWalletAlreadyExists = errors.New("wallet already exists for player and currency")
	// ErrWalletConcurrentModification: the conditional balance update matched
	// no row — the version observed by the use case is stale. Under the wallet
	// row lock this is a defense, not an expected path.
	ErrWalletConcurrentModification = errors.New("wallet was modified concurrently")
)

// Repository writes require an active UnitOfWork transaction in the context:
// financial state must never commit without the ledger, outbox and inbox that
// belong to the same business operation.
type WalletRepository interface {
	InsertWallet(ctx context.Context, state wallet.State) error
	// LockWallet runs SELECT ... FOR UPDATE on the wallet row: concurrent
	// writers serialize per wallet while independent wallets proceed in
	// parallel. It must run inside the unit of work.
	LockWallet(ctx context.Context, id uuid.UUID) (wallet.State, error)
	// UpdateWalletBalance writes the new balance and version only when the
	// row still carries expectedVersion, protecting against lost updates.
	UpdateWalletBalance(ctx context.Context, state wallet.State, expectedVersion int64) error
}

type WagerTransactionRepository interface {
	InsertWagerTransaction(ctx context.Context, state wagering.State) error
	// UpdateWagerTransaction rewrites the mutable columns (status, failure,
	// resolved reference, result, timestamps) of an existing row and its
	// reference retry schedule. The retry command owns the row lock.
	UpdateWagerTransaction(ctx context.Context, state wagering.State, schedule ReferenceSchedule) error
}

type LedgerRepository interface {
	InsertLedgerEntry(ctx context.Context, entry ledger.Entry) error
}

type OutboxRepository interface {
	InsertEvent(ctx context.Context, envelope events.Envelope) error
}
