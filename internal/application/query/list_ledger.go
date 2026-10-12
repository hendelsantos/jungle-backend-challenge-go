package query

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/ports"
)

const (
	// LedgerDefaultLimit matches the contract example (?limit=50).
	LedgerDefaultLimit = 50
	LedgerMaxLimit     = 100
)

var ErrInvalidLimit = errors.New("ledger limit must be between 1 and 100")

type ListLedgerDeps struct {
	Wallets ports.WalletReader
	Ledger  ports.LedgerReader
}

// ListLedger pages the wallet ledger with a stable ordering and an opaque
// cursor. A missing wallet is distinguished from an empty ledger: the former
// returns ErrWalletNotFound, the latter an empty page.
func (d ListLedgerDeps) ListLedger(ctx context.Context, walletID uuid.UUID, cursor string, limit int) (ports.LedgerPage, error) {
	if d.Wallets == nil || d.Ledger == nil {
		return ports.LedgerPage{}, ErrIncompleteDeps
	}
	if walletID == uuid.Nil {
		return ports.LedgerPage{}, ErrInvalidWalletID
	}
	if limit == 0 {
		limit = LedgerDefaultLimit
	}
	if limit < 1 || limit > LedgerMaxLimit {
		return ports.LedgerPage{}, ErrInvalidLimit
	}
	if _, err := d.Wallets.FindWalletByID(ctx, walletID); err != nil {
		return ports.LedgerPage{}, err
	}
	return d.Ledger.ListEntriesByWallet(ctx, walletID, cursor, limit)
}
