// Package wallet owns the invariants of a single wallet in memory.
// Persistence must coordinate concurrent writers and commit movements with the ledger.
package wallet

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/money"
)

var (
	ErrInvalidWallet     = errors.New("invalid wallet")
	ErrInvalidIdentity   = errors.New("wallet and player identities must be non-nil UUIDs")
	ErrNegativeBalance   = errors.New("wallet balance cannot be negative")
	ErrInvalidVersion    = errors.New("wallet version must be positive")
	ErrInvalidTime       = errors.New("wallet timestamps must be nonzero and ordered")
	ErrNegativeAmount    = errors.New("movement amount cannot be negative")
	ErrInsufficientFunds = errors.New("insufficient wallet balance")
	ErrVersionOverflow   = errors.New("wallet version overflow")
)

// Wallet encapsulates its balance and version. It is owned by one use-case
// execution and must not be shared between concurrent goroutines.
type Wallet struct {
	id        uuid.UUID
	playerID  uuid.UUID
	balance   money.Money
	version   int64
	createdAt time.Time
	updatedAt time.Time
}

// State is a detached persistence snapshot, not a mutable view of the aggregate.
// Currency is carried by Balance, so it cannot disagree with the wallet currency.
type State struct {
	ID        uuid.UUID
	PlayerID  uuid.UUID
	Balance   money.Money
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// New creates version 1, including when the initial balance is positive.
// It does not apply a credit, generate events or write an OPENING transaction.
func New(id, playerID uuid.UUID, initialBalance money.Money, at time.Time) (*Wallet, error) {
	return Rehydrate(State{
		ID: id, PlayerID: playerID, Balance: initialBalance,
		Version: 1, CreatedAt: at, UpdatedAt: at,
	})
}

// Rehydrate validates persisted state without replaying movements or changing version.
func Rehydrate(state State) (*Wallet, error) {
	w := &Wallet{
		id: state.ID, playerID: state.PlayerID, balance: state.Balance,
		version: state.Version, createdAt: state.CreatedAt.UTC(), updatedAt: state.UpdatedAt.UTC(),
	}
	if err := w.Validate(); err != nil {
		return nil, err
	}
	return w, nil
}

// Validate also rejects the nil receiver and the uninitialized zero value.
func (w *Wallet) Validate() error {
	if w == nil {
		return ErrInvalidWallet
	}
	if w.id == uuid.Nil || w.playerID == uuid.Nil {
		return fmt.Errorf("%w: %w", ErrInvalidWallet, ErrInvalidIdentity)
	}
	if err := w.balance.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidWallet, err)
	}
	if w.balance.MinorUnits() < 0 {
		return fmt.Errorf("%w: %w", ErrInvalidWallet, ErrNegativeBalance)
	}
	if w.version < 1 {
		return fmt.Errorf("%w: %w", ErrInvalidWallet, ErrInvalidVersion)
	}
	if w.createdAt.IsZero() || w.updatedAt.IsZero() || w.updatedAt.Before(w.createdAt) {
		return fmt.Errorf("%w: %w", ErrInvalidWallet, ErrInvalidTime)
	}
	return nil
}

// Accessors return values, never pointers to the wallet's internal state.
func (w Wallet) ID() uuid.UUID        { return w.id }
func (w Wallet) PlayerID() uuid.UUID  { return w.playerID }
func (w Wallet) Balance() money.Money { return w.balance }
func (w Wallet) Currency() string     { return w.balance.Currency() }
func (w Wallet) Version() int64       { return w.version }
func (w Wallet) CreatedAt() time.Time { return w.createdAt }
func (w Wallet) UpdatedAt() time.Time { return w.updatedAt }

func (w Wallet) Snapshot() State {
	return State{
		ID: w.id, PlayerID: w.playerID, Balance: w.balance,
		Version: w.version, CreatedAt: w.createdAt, UpdatedAt: w.updatedAt,
	}
}

// Debit rejects negative amounts and overdrafts. A zero amount is a validated no-op.
func (w *Wallet) Debit(amount money.Money, at time.Time) error {
	if err := w.validateMovement(amount, at); err != nil {
		return err
	}
	if amount.MinorUnits() > w.balance.MinorUnits() {
		return ErrInsufficientFunds
	}
	balance, err := w.balance.Sub(amount)
	if err != nil {
		return err
	}
	return w.applyBalance(balance, at)
}

// Credit rejects negative amounts and monetary overflow. A zero amount is a no-op.
func (w *Wallet) Credit(amount money.Money, at time.Time) error {
	if err := w.validateMovement(amount, at); err != nil {
		return err
	}
	balance, err := w.balance.Add(amount)
	if err != nil {
		return err
	}
	return w.applyBalance(balance, at)
}

func (w *Wallet) validateMovement(amount money.Money, at time.Time) error {
	if err := w.Validate(); err != nil {
		return err
	}
	if err := amount.Validate(); err != nil {
		return err
	}
	if amount.Currency() != w.Currency() {
		return money.ErrCurrencyMismatch
	}
	if amount.MinorUnits() < 0 {
		return ErrNegativeAmount
	}
	if at.IsZero() || at.Before(w.updatedAt) {
		return ErrInvalidTime
	}
	return nil
}

func (w *Wallet) applyBalance(balance money.Money, at time.Time) error {
	if balance.MinorUnits() == w.balance.MinorUnits() {
		return nil
	}
	if w.version == math.MaxInt64 {
		return ErrVersionOverflow
	}
	// Mutate only after every check has succeeded; errors preserve the entire state.
	w.balance = balance
	w.version++
	w.updatedAt = at.UTC()
	return nil
}
