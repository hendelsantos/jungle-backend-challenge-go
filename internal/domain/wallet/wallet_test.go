package wallet_test

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/money"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wallet"
)

var (
	walletID = uuid.MustParse("0192f291-27dd-7d3f-8071-5f8685deef37")
	playerID = uuid.MustParse("0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1")
	created  = time.Date(2026, 10, 12, 9, 0, 0, 0, time.FixedZone("BRT", -3*60*60))
)

func TestNew(t *testing.T) {
	for _, currency := range []string{money.BRL, money.USD} {
		for _, cents := range []int64{0, 10000, math.MaxInt64} {
			balance := units(t, cents, currency)
			w, err := wallet.New(walletID, playerID, balance, created)
			if err != nil {
				t.Fatal(err)
			}
			if w.ID() != walletID || w.PlayerID() != playerID || w.Balance() != balance || w.Currency() != currency || w.Version() != 1 {
				t.Fatalf("incorrect initial state: %+v", w.Snapshot())
			}
			if w.CreatedAt() != created.UTC() || w.UpdatedAt() != created.UTC() || w.CreatedAt().Location() != time.UTC {
				t.Fatalf("timestamps are not the supplied instant in UTC: %+v", w.Snapshot())
			}
			if err := w.Validate(); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestNewRejectsInvalidState(t *testing.T) {
	for _, tc := range []struct {
		name       string
		id, player uuid.UUID
		balance    money.Money
		at         time.Time
		want       error
	}{
		{"missing wallet", uuid.Nil, playerID, units(t, 0, money.BRL), created, wallet.ErrInvalidIdentity},
		{"missing player", walletID, uuid.Nil, units(t, 0, money.BRL), created, wallet.ErrInvalidIdentity},
		{"uninitialized money", walletID, playerID, money.Money{}, created, money.ErrInvalidMoney},
		{"negative balance", walletID, playerID, units(t, -1, money.BRL), created, wallet.ErrNegativeBalance},
		{"missing time", walletID, playerID, units(t, 0, money.BRL), time.Time{}, wallet.ErrInvalidTime},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, err := wallet.New(tc.id, tc.player, tc.balance, tc.at)
			if w != nil || !errors.Is(err, tc.want) || !errors.Is(err, wallet.ErrInvalidWallet) {
				t.Fatalf("New = %v, %v; want %v", w, err, tc.want)
			}
		})
	}
}

func TestRehydratePreservesStateAndReturnsIndependentWallet(t *testing.T) {
	state := validState(t)
	state.Version = 17
	state.UpdatedAt = created.Add(time.Hour)
	w, err := wallet.Rehydrate(state)
	if err != nil {
		t.Fatal(err)
	}
	want := state
	want.CreatedAt, want.UpdatedAt = state.CreatedAt.UTC(), state.UpdatedAt.UTC()
	if w.Snapshot() != want {
		t.Fatalf("rehydration changed financial state: %+v", w.Snapshot())
	}
	state.Version = 999
	state.Balance = units(t, 999, money.BRL)
	if w.Snapshot() != want {
		t.Fatal("changing the input snapshot mutated the wallet")
	}
	if err := w.Debit(units(t, 100, money.BRL), created.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if w.Version() != 18 || w.Balance().MinorUnits() != want.Balance.MinorUnits()-100 {
		t.Fatalf("movement after rehydration has incorrect result: %+v", w.Snapshot())
	}
}

func TestRehydrateRejectsInvalidState(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*wallet.State)
		want   error
	}{
		{"wallet ID", func(s *wallet.State) { s.ID = uuid.Nil }, wallet.ErrInvalidIdentity},
		{"player ID", func(s *wallet.State) { s.PlayerID = uuid.Nil }, wallet.ErrInvalidIdentity},
		{"money", func(s *wallet.State) { s.Balance = money.Money{} }, money.ErrInvalidMoney},
		{"negative", func(s *wallet.State) { s.Balance = units(t, math.MinInt64, money.BRL) }, wallet.ErrNegativeBalance},
		{"zero version", func(s *wallet.State) { s.Version = 0 }, wallet.ErrInvalidVersion},
		{"negative version", func(s *wallet.State) { s.Version = -1 }, wallet.ErrInvalidVersion},
		{"created time", func(s *wallet.State) { s.CreatedAt = time.Time{} }, wallet.ErrInvalidTime},
		{"updated time", func(s *wallet.State) { s.UpdatedAt = time.Time{} }, wallet.ErrInvalidTime},
		{"reversed times", func(s *wallet.State) { s.UpdatedAt = created.Add(-time.Second) }, wallet.ErrInvalidTime},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := validState(t)
			tc.change(&state)
			w, err := wallet.Rehydrate(state)
			if w != nil || !errors.Is(err, tc.want) {
				t.Fatalf("Rehydrate = %v, %v; want %v", w, err, tc.want)
			}
		})
	}
}

func TestMovements(t *testing.T) {
	w := newWallet(t, 10000)
	for _, tc := range []struct {
		debit                    bool
		amount, balance, version int64
	}{
		{true, 8000, 2000, 2},
		{false, 2500, 4500, 3},
		{true, 4500, 0, 4},
		{false, 0, 0, 4},
		{true, 0, 0, 4},
		{false, math.MaxInt64, math.MaxInt64, 5},
	} {
		at := w.UpdatedAt().Add(time.Second)
		previousTime := w.UpdatedAt()
		operation := w.Credit
		if tc.debit {
			operation = w.Debit
		}
		if err := operation(units(t, tc.amount, money.BRL), at); err != nil {
			t.Fatal(err)
		}
		if w.Balance().MinorUnits() != tc.balance || w.Version() != tc.version {
			t.Fatalf("incorrect movement result: %+v", w.Snapshot())
		}
		wantTime := at.UTC()
		if tc.amount == 0 {
			wantTime = previousTime
		}
		if w.UpdatedAt() != wantTime || w.CreatedAt() != created.UTC() || w.ID() != walletID || w.PlayerID() != playerID {
			t.Fatalf("movement changed identity or timestamps incorrectly: %+v", w.Snapshot())
		}
	}
}

func TestRejectedMovementsPreserveEntireState(t *testing.T) {
	for _, debit := range []bool{false, true} {
		for _, tc := range []struct {
			name   string
			amount money.Money
			at     time.Time
			want   error
		}{
			{"negative", units(t, -1, money.BRL), created, wallet.ErrNegativeAmount},
			{"min amount", units(t, math.MinInt64, money.BRL), created, wallet.ErrNegativeAmount},
			{"uninitialized", money.Money{}, created, money.ErrInvalidMoney},
			{"different currency", units(t, 1, money.USD), created, money.ErrCurrencyMismatch},
			{"zero different currency", units(t, 0, money.USD), created, money.ErrCurrencyMismatch},
			{"missing time", units(t, 1, money.BRL), time.Time{}, wallet.ErrInvalidTime},
			{"old time", units(t, 1, money.BRL), created.Add(-time.Second), wallet.ErrInvalidTime},
		} {
			t.Run(tc.name+map[bool]string{true: "/debit", false: "/credit"}[debit], func(t *testing.T) {
				w := newWallet(t, 10000)
				before := w.Snapshot()
				operation := w.Credit
				if debit {
					operation = w.Debit
				}
				if err := operation(tc.amount, tc.at); !errors.Is(err, tc.want) {
					t.Fatalf("error = %v, want %v", err, tc.want)
				}
				if w.Snapshot() != before {
					t.Fatal("rejected movement changed wallet state")
				}
			})
		}
	}
}

func TestInsufficientBalanceAndOverflow(t *testing.T) {
	w := newWallet(t, 10000)
	if err := w.Debit(units(t, 8000, money.BRL), created); err != nil {
		t.Fatal(err)
	}
	before := w.Snapshot()
	if err := w.Debit(units(t, 8000, money.BRL), created); !errors.Is(err, wallet.ErrInsufficientFunds) {
		t.Fatalf("second debit error = %v", err)
	}
	if w.Snapshot() != before || w.Balance().MinorUnits() != 2000 {
		t.Fatal("insufficient balance rejection changed state")
	}
	w = newWallet(t, math.MaxInt64)
	before = w.Snapshot()
	if err := w.Credit(units(t, 1, money.BRL), created); !errors.Is(err, money.ErrOverflow) || w.Snapshot() != before {
		t.Fatalf("credit overflow changed state or returned wrong error: %v", err)
	}
	w = newWallet(t, 0)
	before = w.Snapshot()
	if err := w.Debit(units(t, 1, money.BRL), created); !errors.Is(err, wallet.ErrInsufficientFunds) || w.Snapshot() != before {
		t.Fatalf("debit from zero changed state or returned wrong error: %v", err)
	}
}

func TestVersionLimit(t *testing.T) {
	state := validState(t)
	state.Version = math.MaxInt64 - 1
	w, err := wallet.Rehydrate(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Credit(units(t, 1, money.BRL), created); err != nil || w.Version() != math.MaxInt64 {
		t.Fatalf("could not reach max version: %v", err)
	}
	before := w.Snapshot()
	for _, operation := range []func(money.Money, time.Time) error{w.Credit, w.Debit} {
		if err := operation(units(t, 1, money.BRL), created); !errors.Is(err, wallet.ErrVersionOverflow) {
			t.Fatalf("version overflow error = %v", err)
		}
		if err := operation(units(t, 0, money.BRL), created.Add(time.Hour)); err != nil {
			t.Fatalf("zero movement should not consume a version: %v", err)
		}
		if w.Snapshot() != before {
			t.Fatal("version overflow or zero movement changed state")
		}
	}
}

func TestInvalidReceiver(t *testing.T) {
	for _, w := range []*wallet.Wallet{nil, {}} {
		for _, err := range []error{w.Validate(), w.Debit(units(t, 0, money.BRL), created), w.Credit(units(t, 0, money.BRL), created)} {
			if !errors.Is(err, wallet.ErrInvalidWallet) {
				t.Fatalf("error = %v, want invalid wallet", err)
			}
		}
	}
}

func TestReadAccessDoesNotExposeMutableState(t *testing.T) {
	w := newWallet(t, 10000)
	before := w.Snapshot()
	balance := w.Balance()
	if err := json.Unmarshal([]byte(`{"amount":"1","currency":"USD"}`), &balance); err != nil {
		t.Fatal(err)
	}
	snapshot := w.Snapshot()
	snapshot.Balance = balance
	snapshot.ID[0] = 0
	snapshot.Version++
	if w.Snapshot() != before {
		t.Fatal("mutating accessor results changed the wallet")
	}
}

func units(t testing.TB, cents int64, currency string) money.Money {
	t.Helper()
	m, err := money.FromMinorUnits(cents, currency)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func newWallet(t testing.TB, cents int64) *wallet.Wallet {
	t.Helper()
	w, err := wallet.New(walletID, playerID, units(t, cents, money.BRL), created)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func validState(t testing.TB) wallet.State {
	t.Helper()
	return wallet.State{
		ID: walletID, PlayerID: playerID, Balance: units(t, 10000, money.BRL),
		Version: 1, CreatedAt: created, UpdatedAt: created,
	}
}
