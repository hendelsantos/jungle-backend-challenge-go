package postgres

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/ports"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/money"
)

var _ ports.ReconciliationReader = (*Reconciler)(nil)

type Reconciler struct {
	pool *pgxpool.Pool
}

func NewReconciler(pool *pgxpool.Pool) *Reconciler {
	return &Reconciler{pool: pool}
}

// ReconcileWallet reads the stored balance and the ledger sum inside one
// REPEATABLE READ READ ONLY snapshot, so concurrent movements cannot make the
// comparison mix two instants. It never writes: corrections require new
// ledger entries by contract.
func (r *Reconciler) ReconcileWallet(ctx context.Context, walletID uuid.UUID) (ports.ReconciliationReport, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return ports.ReconciliationReport{}, fmt.Errorf("postgres: begin reconciliation snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // harmless after commit; read-only never dirties

	var (
		currency     string
		balanceCents int64
	)
	err = tx.QueryRow(ctx, `
		SELECT currency, balance_cents
		FROM wallets
		WHERE id = $1`, walletID).Scan(&currency, &balanceCents)
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.ReconciliationReport{}, ports.ErrWalletNotFound
	}
	if err != nil {
		return ports.ReconciliationReport{}, fmt.Errorf("postgres: load wallet for reconciliation: %w", err)
	}

	var (
		checkedEntries int64
		calculatedText string
	)
	// SUM over bigint returns numeric: the addition cannot overflow silently.
	// The conversion to int64 happens in Go after an exact big.Int parse.
	err = tx.QueryRow(ctx, `
		SELECT count(*),
		       COALESCE(SUM(CASE direction WHEN 'CREDIT' THEN amount_cents ELSE -amount_cents END), 0)::text
		FROM wallet_ledger_entries
		WHERE wallet_id = $1`, walletID).Scan(&checkedEntries, &calculatedText)
	if err != nil {
		return ports.ReconciliationReport{}, fmt.Errorf("postgres: sum ledger: %w", err)
	}

	calculated := new(big.Int)
	if _, ok := calculated.SetString(calculatedText, 10); !ok {
		return ports.ReconciliationReport{}, fmt.Errorf("postgres: parse ledger sum %q for wallet %s", calculatedText, walletID)
	}
	difference := new(big.Int).Sub(big.NewInt(balanceCents), calculated)
	if !calculated.IsInt64() || !difference.IsInt64() {
		return ports.ReconciliationReport{}, fmt.Errorf("postgres: wallet %s: %w", walletID, ports.ErrLedgerSumOutOfRange)
	}

	stored, err := money.FromMinorUnits(balanceCents, currency)
	if err != nil {
		return ports.ReconciliationReport{}, fmt.Errorf("postgres: wallet %s stored balance: %w", walletID, err)
	}
	calculatedBalance, err := money.FromMinorUnits(calculated.Int64(), currency)
	if err != nil {
		return ports.ReconciliationReport{}, fmt.Errorf("postgres: wallet %s calculated balance: %w", walletID, err)
	}
	differenceBalance, err := money.FromMinorUnits(difference.Int64(), currency)
	if err != nil {
		return ports.ReconciliationReport{}, fmt.Errorf("postgres: wallet %s difference: %w", walletID, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return ports.ReconciliationReport{}, fmt.Errorf("postgres: commit reconciliation snapshot: %w", err)
	}
	return ports.ReconciliationReport{
		WalletID:          walletID,
		StoredBalance:     stored,
		CalculatedBalance: calculatedBalance,
		Difference:        differenceBalance,
		CheckedEntries:    checkedEntries,
		Consistent:        difference.Sign() == 0,
	}, nil
}
