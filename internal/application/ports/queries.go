package ports

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/ledger"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/money"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wallet"
)

var (
	// ErrWalletNotFound lets the HTTP layer answer 404 without leaking scope.
	ErrWalletNotFound = errors.New("wallet not found")
	// ErrInvalidCursor marks an unparsable pagination cursor (future 400).
	ErrInvalidCursor = errors.New("invalid ledger cursor")
	// ErrLedgerSumOutOfRange: the ledger sum does not fit a representable
	// balance; reconciliation fails loudly instead of truncating.
	ErrLedgerSumOutOfRange = errors.New("ledger sum does not fit a representable balance")
)

// Read ports may run on the pool or join the transaction carried by the
// context when a use case already opened one.
type WalletReader interface {
	FindWalletByID(ctx context.Context, id uuid.UUID) (wallet.State, error)
}

// LedgerPage: NextCursor is empty when there are no further entries.
type LedgerPage struct {
	Entries    []ledger.Entry
	NextCursor string
}

type LedgerReader interface {
	ListEntriesByWallet(ctx context.Context, walletID uuid.UUID, cursor string, limit int) (LedgerPage, error)
}

// ReconciliationReport compares the stored balance with the ledger sum from
// one consistent snapshot. Difference is stored minus calculated.
type ReconciliationReport struct {
	WalletID          uuid.UUID
	StoredBalance     money.Money
	CalculatedBalance money.Money
	Difference        money.Money
	CheckedEntries    int64
	Consistent        bool
}

type ReconciliationReader interface {
	ReconcileWallet(ctx context.Context, walletID uuid.UUID) (ReconciliationReport, error)
}
