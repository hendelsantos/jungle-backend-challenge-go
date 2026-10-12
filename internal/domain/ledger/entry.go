// Package ledger holds the immutable financial records: exactly one entry per
// effective wallet movement. Uniqueness of (wallet, transaction) and append-only
// enforcement are the database schema's responsibility in a later phase.
package ledger

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/money"
)

// Direction mirrors the wallet movement that produced the entry.
type Direction string

const (
	Debit  Direction = "DEBIT"
	Credit Direction = "CREDIT"
)

var (
	ErrInvalidEntry      = errors.New("invalid ledger entry")
	ErrInvalidDirection  = errors.New("invalid ledger direction")
	ErrInvalidTime       = errors.New("invalid ledger timestamp")
	ErrNonPositiveAmount = errors.New("ledger amount must be positive")
	ErrNegativeBalance   = errors.New("ledger balance cannot be negative")
	ErrBalanceMismatch   = errors.New("ledger balance after does not match direction applied to balance before")
)

// Entry is an immutable value: there are no setters and no transitions, so a
// single constructor serves both creation and rehydration. The zero value is
// invalid. LOSS and rejected operations must not produce entries at all.
type Entry struct {
	id            uuid.UUID
	walletID      uuid.UUID
	transactionID uuid.UUID
	direction     Direction
	amount        money.Money
	balanceBefore money.Money
	balanceAfter  money.Money
	createdAt     time.Time
}

// New validates balanceAfter = balanceBefore ± amount according to direction.
// The timestamp is stored in UTC preserving the supplied instant.
func New(id, walletID, transactionID uuid.UUID, direction Direction, amount, balanceBefore, balanceAfter money.Money, at time.Time) (Entry, error) {
	e := Entry{
		id:            id,
		walletID:      walletID,
		transactionID: transactionID,
		direction:     direction,
		amount:        amount,
		balanceBefore: balanceBefore,
		balanceAfter:  balanceAfter,
		createdAt:     at.UTC(),
	}
	if err := e.Validate(); err != nil {
		return Entry{}, err
	}
	return e, nil
}

// Validate also rejects the zero value, unknown directions and any arithmetic
// disagreement between the balances, the amount and the direction.
func (e Entry) Validate() error {
	if e.id == uuid.Nil || e.walletID == uuid.Nil || e.transactionID == uuid.Nil {
		return ErrInvalidEntry
	}
	switch e.direction {
	case Debit, Credit:
	default:
		return ErrInvalidDirection
	}
	if e.createdAt.IsZero() {
		return ErrInvalidTime
	}
	for _, value := range []money.Money{e.amount, e.balanceBefore, e.balanceAfter} {
		if err := value.Validate(); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidEntry, err)
		}
	}
	if e.amount.MinorUnits() <= 0 {
		return ErrNonPositiveAmount
	}
	if e.balanceBefore.MinorUnits() < 0 || e.balanceAfter.MinorUnits() < 0 {
		return ErrNegativeBalance
	}
	expected, err := e.balanceAfterFor()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidEntry, err)
	}
	equal, err := expected.Equal(e.balanceAfter)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidEntry, err)
	}
	if !equal {
		return ErrBalanceMismatch
	}
	return nil
}

func (e Entry) balanceAfterFor() (money.Money, error) {
	if e.direction == Credit {
		return e.balanceBefore.Add(e.amount)
	}
	return e.balanceBefore.Sub(e.amount)
}

// Accessors return values; an entry cannot be mutated through them.
func (e Entry) ID() uuid.UUID              { return e.id }
func (e Entry) WalletID() uuid.UUID        { return e.walletID }
func (e Entry) TransactionID() uuid.UUID   { return e.transactionID }
func (e Entry) Direction() Direction       { return e.direction }
func (e Entry) Amount() money.Money        { return e.amount }
func (e Entry) BalanceBefore() money.Money { return e.balanceBefore }
func (e Entry) BalanceAfter() money.Money  { return e.balanceAfter }
func (e Entry) CreatedAt() time.Time       { return e.createdAt }
