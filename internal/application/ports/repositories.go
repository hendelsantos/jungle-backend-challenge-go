package ports

import (
	"context"
	"errors"

	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/events"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/ledger"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wagering"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wallet"
)

// ErrWalletAlreadyExists marks the UNIQUE(player_id, currency) conflict; HTTP
// will translate it to 409.
var ErrWalletAlreadyExists = errors.New("wallet already exists for player and currency")

// Repository writes require an active UnitOfWork transaction in the context:
// financial state must never commit without the ledger, outbox and inbox that
// belong to the same business operation.
type WalletRepository interface {
	InsertWallet(ctx context.Context, state wallet.State) error
}

type WagerTransactionRepository interface {
	InsertWagerTransaction(ctx context.Context, state wagering.State) error
}

type LedgerRepository interface {
	InsertLedgerEntry(ctx context.Context, entry ledger.Entry) error
}

type OutboxRepository interface {
	InsertEvent(ctx context.Context, envelope events.Envelope) error
}
