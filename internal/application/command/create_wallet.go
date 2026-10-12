// Package command implements the use cases shared by the HTTP and SQS
// adapters. Business rules live in the domain; this package only orchestrates.
package command

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/ports"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/events"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/ledger"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/money"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wagering"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wallet"
)

var (
	ErrIncompleteDeps     = errors.New("create wallet dependencies are incomplete")
	ErrInvalidPlayerID    = errors.New("player id must be a non-nil uuid")
	ErrInvalidCorrelation = errors.New("correlation id must be a non-nil uuid")
	ErrInvalidAmount      = errors.New("initial balance must be a valid non-negative amount")
	// The domain accepts BRL and USD; the application restricts wallets to BRL,
	// as allowed by the challenge statement.
	ErrCurrencyNotAccepted = errors.New("wallets accept BRL only")
)

type CreateWalletInput struct {
	PlayerID       uuid.UUID
	InitialBalance money.Money
	CorrelationID  uuid.UUID
}

type CreateWalletResult struct {
	WalletID uuid.UUID
	PlayerID uuid.UUID
	Balance  money.Money
	Version  int64
}

// CreateWalletDeps carries the ports plus injected identity and clock sources,
// so tests are deterministic without faking infrastructure semantics.
type CreateWalletDeps struct {
	UnitOfWork ports.UnitOfWork
	Wallets    ports.WalletRepository
	Wagers     ports.WagerTransactionRepository
	Ledger     ports.LedgerRepository
	Outbox     ports.OutboxRepository
	Now        func() time.Time
	NewID      func() uuid.UUID
}

// CreateWallet commits the wallet and, for a positive initial balance, the
// OPENING transaction, its credit ledger entry and the WagerTransactionProcessed
// and WalletBalanceChanged outbox events in a single SQL transaction. A zero
// initial balance creates only the wallet, as the contract requires.
func (d CreateWalletDeps) CreateWallet(ctx context.Context, input CreateWalletInput) (CreateWalletResult, error) {
	if err := d.validate(); err != nil {
		return CreateWalletResult{}, err
	}
	if err := validateInput(input); err != nil {
		return CreateWalletResult{}, err
	}
	now := d.Now().UTC()
	walletID, openingID := d.NewID(), d.NewID()
	newWallet, err := wallet.New(walletID, input.PlayerID, input.InitialBalance, now)
	if err != nil {
		return CreateWalletResult{}, fmt.Errorf("create wallet aggregate: %w", err)
	}
	err = d.UnitOfWork.WithinTx(ctx, func(ctx context.Context) error {
		if err := d.Wallets.InsertWallet(ctx, newWallet.Snapshot()); err != nil {
			return err
		}
		if input.InitialBalance.MinorUnits() == 0 {
			return nil
		}
		opening, err := wagering.NewOpening(openingID, walletID, input.PlayerID, input.InitialBalance, now)
		if err != nil {
			return fmt.Errorf("build opening transaction: %w", err)
		}
		// The wallet already holds its initial balance; Process only records
		// the PROCESSED result, it never credits a second time.
		if err := opening.Process(newWallet, wagering.Reference{}, now); err != nil {
			return fmt.Errorf("process opening: %w", err)
		}
		if err := d.Wagers.InsertWagerTransaction(ctx, opening.Snapshot()); err != nil {
			return err
		}
		before, err := money.Zero(input.InitialBalance.Currency())
		if err != nil {
			return err
		}
		entry, err := ledger.New(d.NewID(), walletID, openingID, ledger.Credit,
			input.InitialBalance, before, input.InitialBalance, now)
		if err != nil {
			return fmt.Errorf("build ledger entry: %w", err)
		}
		if err := d.Ledger.InsertLedgerEntry(ctx, entry); err != nil {
			return err
		}
		// The opening transaction is the cause of both events; the correlation
		// identifier follows the request that triggered the use case.
		processed, err := events.NewWagerTransactionProcessed(d.NewID(), input.CorrelationID, openingID, now,
			events.WagerTransactionProcessedData{
				TransactionID: openingID,
				WalletID:      walletID,
				PlayerID:      input.PlayerID,
				Kind:          wagering.Opening,
				Money:         input.InitialBalance,
			})
		if err != nil {
			return fmt.Errorf("build processed event: %w", err)
		}
		balanceChanged, err := events.NewWalletBalanceChanged(d.NewID(), input.CorrelationID, openingID, now,
			events.WalletBalanceChangedData{
				WalletID:      walletID,
				TransactionID: openingID,
				Direction:     ledger.Credit,
				Money:         input.InitialBalance,
				BalanceBefore: before,
				BalanceAfter:  input.InitialBalance,
				WalletVersion: newWallet.Version(),
			})
		if err != nil {
			return fmt.Errorf("build balance event: %w", err)
		}
		if err := d.Outbox.InsertEvent(ctx, processed); err != nil {
			return err
		}
		return d.Outbox.InsertEvent(ctx, balanceChanged)
	})
	if err != nil {
		return CreateWalletResult{}, err
	}
	return CreateWalletResult{
		WalletID: walletID,
		PlayerID: input.PlayerID,
		Balance:  input.InitialBalance,
		Version:  newWallet.Version(),
	}, nil
}

func (d CreateWalletDeps) validate() error {
	if d.UnitOfWork == nil || d.Wallets == nil || d.Wagers == nil ||
		d.Ledger == nil || d.Outbox == nil || d.Now == nil || d.NewID == nil {
		return ErrIncompleteDeps
	}
	return nil
}

func validateInput(input CreateWalletInput) error {
	if input.PlayerID == uuid.Nil {
		return ErrInvalidPlayerID
	}
	if input.CorrelationID == uuid.Nil {
		return ErrInvalidCorrelation
	}
	if err := input.InitialBalance.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidAmount, err)
	}
	if input.InitialBalance.MinorUnits() < 0 {
		return ErrInvalidAmount
	}
	if input.InitialBalance.Currency() != money.BRL {
		return fmt.Errorf("%w: %s", ErrCurrencyNotAccepted, input.InitialBalance.Currency())
	}
	return nil
}
