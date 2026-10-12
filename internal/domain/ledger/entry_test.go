package ledger

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/money"
)

func brl(cents int64) money.Money {
	value, err := money.FromMinorUnits(cents, money.BRL)
	if err != nil {
		panic(err)
	}
	return value
}

type entryInput struct {
	id, walletID, transactionID uuid.UUID
	direction                   Direction
	amount, before, after       money.Money
	at                          time.Time
}

func validInput() entryInput {
	return entryInput{
		id:            uuid.New(),
		walletID:      uuid.New(),
		transactionID: uuid.New(),
		direction:     Debit,
		amount:        brl(2500),
		before:        brl(10000),
		after:         brl(7500),
		at:            time.Date(2026, 10, 12, 14, 0, 0, 0, time.FixedZone("UTC+2", 2*60*60)),
	}
}

func TestNewValidEntries(t *testing.T) {
	cases := []struct {
		name      string
		direction Direction
		amount    int64
		before    int64
		after     int64
	}{
		{"debit", Debit, 2500, 10000, 7500},
		{"credit", Credit, 2500, 10000, 12500},
		{"debit to zero", Debit, 10000, 10000, 0},
		{"credit from zero", Credit, 2500, 0, 2500},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := validInput()
			in.direction = tc.direction
			in.amount = brl(tc.amount)
			in.before = brl(tc.before)
			in.after = brl(tc.after)

			entry, err := New(in.id, in.walletID, in.transactionID, in.direction, in.amount, in.before, in.after, in.at)
			if err != nil {
				t.Fatalf("New(): %v", err)
			}
			if entry.ID() != in.id || entry.WalletID() != in.walletID || entry.TransactionID() != in.transactionID {
				t.Fatalf("identity mismatch: got %v/%v/%v", entry.ID(), entry.WalletID(), entry.TransactionID())
			}
			if entry.Direction() != tc.direction {
				t.Fatalf("direction: got %q, want %q", entry.Direction(), tc.direction)
			}
			if entry.Amount() != in.amount || entry.BalanceBefore() != in.before || entry.BalanceAfter() != in.after {
				t.Fatalf("money mismatch: got %v/%v/%v", entry.Amount(), entry.BalanceBefore(), entry.BalanceAfter())
			}
			if !entry.CreatedAt().Equal(in.at) {
				t.Fatalf("createdAt instant changed: got %v, want %v", entry.CreatedAt(), in.at)
			}
			if got := entry.CreatedAt(); got.Location() != time.UTC || got.Hour() != 12 {
				t.Fatalf("createdAt not stored in UTC: got %v", entry.CreatedAt())
			}
			if err := entry.Validate(); err != nil {
				t.Fatalf("Validate(): %v", err)
			}

			again, err := New(in.id, in.walletID, in.transactionID, in.direction, in.amount, in.before, in.after, in.at)
			if err != nil {
				t.Fatalf("New() second call: %v", err)
			}
			if again != entry {
				t.Fatalf("equal inputs must produce equal values: %v != %v", again, entry)
			}
		})
	}
}

func TestNewSupportsAnySupportedCurrency(t *testing.T) {
	amount, err := money.FromMinorUnits(2500, money.USD)
	if err != nil {
		t.Fatalf("FromMinorUnits: %v", err)
	}
	before, err := money.FromMinorUnits(10000, money.USD)
	if err != nil {
		t.Fatalf("FromMinorUnits: %v", err)
	}
	after, err := money.FromMinorUnits(7500, money.USD)
	if err != nil {
		t.Fatalf("FromMinorUnits: %v", err)
	}
	if _, err := New(uuid.New(), uuid.New(), uuid.New(), Debit, amount, before, after, time.Now().UTC()); err != nil {
		t.Fatalf("New() with USD: %v", err)
	}
}

func TestNewRejectsInvalidEntries(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(in *entryInput)
		wantErr error
	}{
		{"nil entry id", func(in *entryInput) { in.id = uuid.Nil }, ErrInvalidEntry},
		{"nil wallet id", func(in *entryInput) { in.walletID = uuid.Nil }, ErrInvalidEntry},
		{"nil transaction id", func(in *entryInput) { in.transactionID = uuid.Nil }, ErrInvalidEntry},
		{"unknown direction", func(in *entryInput) { in.direction = Direction("debit") }, ErrInvalidDirection},
		{"empty direction", func(in *entryInput) { in.direction = Direction("") }, ErrInvalidDirection},
		{"zero timestamp", func(in *entryInput) { in.at = time.Time{} }, ErrInvalidTime},
		{"zero amount", func(in *entryInput) { in.amount = brl(0) }, ErrNonPositiveAmount},
		{"negative amount", func(in *entryInput) { in.amount = brl(-2500) }, ErrNonPositiveAmount},
		{"uninitialized amount", func(in *entryInput) { in.amount = money.Money{} }, money.ErrInvalidMoney},
		{"negative balance before", func(in *entryInput) { in.before = brl(-100); in.amount = brl(100); in.after = brl(0) }, ErrNegativeBalance},
		{"negative balance after", func(in *entryInput) { in.after = brl(-7500) }, ErrNegativeBalance},
		{"debit arithmetic mismatch", func(in *entryInput) { in.after = brl(7000) }, ErrBalanceMismatch},
		{"credit arithmetic mismatch", func(in *entryInput) {
			in.direction = Credit
			in.after = brl(7000)
		}, ErrBalanceMismatch},
		{"debit larger than balance before", func(in *entryInput) { in.amount = brl(20000); in.after = brl(0) }, ErrBalanceMismatch},
		{"amount currency mismatch", func(in *entryInput) {
			amount, err := money.FromMinorUnits(2500, money.USD)
			if err != nil {
				t.Fatalf("FromMinorUnits: %v", err)
			}
			in.amount = amount
		}, money.ErrCurrencyMismatch},
		{"balance after currency mismatch", func(in *entryInput) {
			after, err := money.FromMinorUnits(7500, money.USD)
			if err != nil {
				t.Fatalf("FromMinorUnits: %v", err)
			}
			in.after = after
		}, money.ErrCurrencyMismatch},
		{"credit overflow", func(in *entryInput) {
			in.direction = Credit
			in.before = brl(math.MaxInt64)
			in.amount = brl(100)
			in.after = brl(math.MaxInt64)
		}, money.ErrOverflow},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := validInput()
			tc.mutate(&in)

			entry, err := New(in.id, in.walletID, in.transactionID, in.direction, in.amount, in.before, in.after, in.at)
			if err == nil {
				t.Fatalf("New(): expected error %v, got valid entry %v", tc.wantErr, entry)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("New(): got %v, want %v", err, tc.wantErr)
			}
			if entry != (Entry{}) {
				t.Fatalf("rejected entries must return the zero value, got %v", entry)
			}
		})
	}
}

func TestZeroValueIsInvalid(t *testing.T) {
	if err := (Entry{}).Validate(); !errors.Is(err, ErrInvalidEntry) {
		t.Fatalf("Entry{}.Validate(): got %v, want %v", err, ErrInvalidEntry)
	}
}
