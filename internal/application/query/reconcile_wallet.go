package query

import (
	"context"

	"github.com/google/uuid"

	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/ports"
)

type ReconcileWalletDeps struct {
	Reconciliation ports.ReconciliationReader
}

// ReconcileWallet compares the stored balance with the ledger sum inside one
// consistent snapshot and reports the difference without altering anything.
func (d ReconcileWalletDeps) ReconcileWallet(ctx context.Context, walletID uuid.UUID) (ports.ReconciliationReport, error) {
	if d.Reconciliation == nil {
		return ports.ReconciliationReport{}, ErrIncompleteDeps
	}
	if walletID == uuid.Nil {
		return ports.ReconciliationReport{}, ErrInvalidWalletID
	}
	return d.Reconciliation.ReconcileWallet(ctx, walletID)
}
