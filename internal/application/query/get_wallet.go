// Package query holds the read-only use cases; none of them mutates
// financial state. Business rules stay in the domain.
package query

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/ports"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wallet"
)

var (
	ErrIncompleteDeps  = errors.New("query dependencies are incomplete")
	ErrInvalidWalletID = errors.New("wallet id must be a non-nil uuid")
)

type GetWalletDeps struct {
	Wallets ports.WalletReader
}

func (d GetWalletDeps) GetWallet(ctx context.Context, walletID uuid.UUID) (wallet.State, error) {
	if d.Wallets == nil {
		return wallet.State{}, ErrIncompleteDeps
	}
	if walletID == uuid.Nil {
		return wallet.State{}, ErrInvalidWalletID
	}
	return d.Wallets.FindWalletByID(ctx, walletID)
}
