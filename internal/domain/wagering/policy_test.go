package wagering_test

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/money"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wagering"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wallet"
)

func TestOperationDirectionsAndResults(t *testing.T) {
	for _, tc := range []struct {
		name             string
		kind             wagering.Kind
		amount           int64
		ref              *wagering.WagerTransaction
		balance, version int64
	}{
		{"bet", wagering.Bet, 2500, nil, 7500, 2},
		{"win", wagering.Win, 2500, nil, 12500, 2},
		{"loss", wagering.Loss, 0, nil, 10000, 1},
		{"refund", wagering.Refund, 2500, reference(t, wagering.Bet, 2500), 12500, 2},
		{"rollback bet", wagering.Rollback, 2500, reference(t, wagering.Bet, 2500), 12500, 2},
		{"rollback win", wagering.Rollback, 2500, reference(t, wagering.Win, 2500), 7500, 2},
		{"rollback refund", wagering.Rollback, 2500, reference(t, wagering.Refund, 2500), 7500, 2},
		{"referenced win with different payout", wagering.Win, 5000, reference(t, wagering.Bet, 2500), 15000, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWallet(t, 10000)
			walletBefore := w.Snapshot()
			r := request(t, tc.kind, tc.amount, "operation")
			if tc.ref != nil {
				r.ReferenceExternalTransactionID = "reference"
			}
			tx := newTransaction(t, r)
			if err := tx.Process(w, wagering.Reference{Transaction: tc.ref}, at.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			if tx.Status() != wagering.Processed || w.Balance().MinorUnits() != tc.balance || w.Version() != tc.version {
				t.Fatalf("wrong outcome: %+v %+v", tx.Snapshot(), w.Snapshot())
			}
			if tc.kind == wagering.Loss && w.Snapshot() != walletBefore {
				t.Fatal("LOSS changed wallet state")
			}
			result, ok := tx.Result()
			if !ok || result.Balance != w.Balance() || result.WalletVersion != tc.version {
				t.Fatal("missing original financial result")
			}
			before := tx.Snapshot()
			if err := w.Credit(units(t, 1), at.Add(2*time.Second)); err != nil {
				t.Fatal(err)
			}
			if tx.Snapshot() != before {
				t.Fatal("later movement changed persisted result")
			}
			if tc.kind == wagering.Loss && before.Result.WalletVersion != 1 {
				t.Fatal("LOSS changed version")
			}
		})
	}
}

func TestBusinessRejections(t *testing.T) {
	for _, tc := range []struct {
		name            string
		kind            wagering.Kind
		initial, amount int64
		ref             *wagering.WagerTransaction
		compensated     bool
		code            wagering.FailureCode
	}{
		{"bet insufficient", wagering.Bet, 10000, 20000, nil, false, wagering.InsufficientFunds},
		{"win reversal insufficient", wagering.Rollback, 0, 2500, reference(t, wagering.Win, 2500), false, wagering.ReversalInsufficientFunds},
		{"refund reversal insufficient", wagering.Rollback, 0, 2500, reference(t, wagering.Refund, 2500), false, wagering.ReversalInsufficientFunds},
		{"win overflow", wagering.Win, math.MaxInt64, 1, nil, false, wagering.BalanceOverflow},
		{"refund overflow", wagering.Refund, math.MaxInt64, 1, reference(t, wagering.Bet, 1), false, wagering.BalanceOverflow},
		{"refund amount", wagering.Refund, 10000, 1, reference(t, wagering.Bet, 2), false, wagering.ReferenceAmountMismatch},
		{"rollback amount", wagering.Rollback, 10000, 1, reference(t, wagering.Win, 2), false, wagering.ReferenceAmountMismatch},
		{"refund win", wagering.Refund, 10000, 1, reference(t, wagering.Win, 1), false, wagering.InvalidReferenceKind},
		{"rollback loss", wagering.Rollback, 10000, 1, reference(t, wagering.Loss, 0), false, wagering.InvalidReferenceKind},
		{"refund already reversed", wagering.Refund, 10000, 1, reference(t, wagering.Bet, 1), true, wagering.AlreadyReversed},
		{"rollback already refunded", wagering.Rollback, 10000, 1, reference(t, wagering.Bet, 1), true, wagering.AlreadyReversed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWallet(t, tc.initial)
			before := w.Snapshot()
			tx := newTransaction(t, request(t, tc.kind, tc.amount, "operation"))
			if err := tx.Process(w, wagering.Reference{Transaction: tc.ref, Compensated: tc.compensated}, at); err != nil {
				t.Fatal(err)
			}
			if tx.Status() != wagering.Rejected || tx.FailureCode() != tc.code || w.Snapshot() != before {
				t.Fatalf("incorrect rejection: %+v", tx.Snapshot())
			}
		})
	}
}

func TestReferenceScope(t *testing.T) {
	for name, change := range map[string]func(*wagering.State){
		"provider":    func(s *wagering.State) { s.Request.ProviderID = "provider-b" },
		"wallet":      func(s *wagering.State) { s.Request.WalletID = id("other-wallet") },
		"player":      func(s *wagering.State) { s.Request.PlayerID = id("other-player") },
		"round":       func(s *wagering.State) { s.Request.RoundID = "other-round" },
		"external ID": func(s *wagering.State) { s.Request.ExternalTransactionID = "other-reference" },
		"currency": func(s *wagering.State) {
			s.Request.Money, _ = money.FromMinorUnits(100, money.USD)
			s.Result.Balance, _ = money.Zero(money.USD)
		},
		"self internal ID": func(s *wagering.State) { s.Request.ID = id("operation") },
	} {
		for _, kind := range []wagering.Kind{wagering.Win, wagering.Refund, wagering.Rollback} {
			t.Run(name+string(kind), func(t *testing.T) {
				s := reference(t, wagering.Bet, 100).Snapshot()
				change(&s)
				ref, err := wagering.Rehydrate(s)
				if err != nil {
					t.Fatal(err)
				}
				r := request(t, kind, 100, "operation")
				r.ReferenceExternalTransactionID = "reference"
				tx := newTransaction(t, r)
				w := newWallet(t, 10000)
				before := w.Snapshot()
				if err := tx.Process(w, wagering.Reference{Transaction: ref}, at); err != nil {
					t.Fatal(err)
				}
				if tx.Status() != wagering.Rejected || tx.FailureCode() != wagering.ReferenceScopeMismatch || w.Snapshot() != before {
					t.Fatalf("scope mismatch accepted: %+v", tx.Snapshot())
				}
			})
		}
	}
}

func TestPendingReferenceResolutionAndExpiry(t *testing.T) {
	for _, kind := range []wagering.Kind{wagering.Win, wagering.Refund, wagering.Rollback} {
		r := request(t, kind, 100, "operation")
		r.ReferenceExternalTransactionID = "reference"
		tx := newTransaction(t, r)
		w := newWallet(t, 10000)
		before := w.Snapshot()
		for _, ref := range []*wagering.WagerTransaction{nil, newTransaction(t, request(t, wagering.Bet, 100, "reference"))} {
			if err := tx.Process(w, wagering.Reference{Transaction: ref}, at); err != nil {
				t.Fatal(err)
			}
			if tx.Status() != wagering.PendingReference || tx.Snapshot().HasResult || w.Snapshot() != before {
				t.Fatal("pending reference changed finances")
			}
		}
		restored, err := wagering.Rehydrate(tx.Snapshot())
		if err != nil {
			t.Fatal(err)
		}
		if err := restored.Process(w, wagering.Reference{Transaction: reference(t, wagering.Bet, 100)}, at.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if restored.Status() != wagering.Processed || w.Balance().MinorUnits() != 10100 {
			t.Fatal("reference did not resume")
		}
		if err := tx.Reject(wagering.ReferenceNotFound, at.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if tx.Status() != wagering.Rejected || tx.Snapshot().HasResult {
			t.Fatal("expiry invented a result")
		}
	}
	for _, failed := range []bool{false, true} {
		ref := newTransaction(t, request(t, wagering.Bet, 100, "reference"))
		var err error
		if failed {
			err = ref.FailPermanent(at)
		} else {
			err = ref.Reject(wagering.InsufficientFunds, at)
		}
		if err != nil {
			t.Fatal(err)
		}
		tx := newTransaction(t, request(t, wagering.Refund, 100, "operation"))
		if err := tx.Process(newWallet(t, 10000), wagering.Reference{Transaction: ref}, at); err != nil {
			t.Fatal(err)
		}
		if tx.FailureCode() != wagering.ReferenceNotProcessed {
			t.Fatal("terminal reference was not rejected")
		}
	}
}

func TestCompensationChain(t *testing.T) {
	w := newWallet(t, 10000)
	bet := newTransaction(t, request(t, wagering.Bet, 2500, "bet"))
	if err := bet.Process(w, wagering.Reference{}, at); err != nil {
		t.Fatal(err)
	}
	r := request(t, wagering.Refund, 2500, "refund")
	r.ReferenceExternalTransactionID = "bet"
	refund := newTransaction(t, r)
	betBefore := bet.Snapshot()
	if err := refund.Process(w, wagering.Reference{Transaction: bet}, at); err != nil {
		t.Fatal(err)
	}
	if w.Balance().MinorUnits() != 10000 || bet.Snapshot() != betBefore {
		t.Fatal("refund changed reference or wrong balance")
	}
	r = request(t, wagering.Rollback, 2500, "rollback-refund")
	r.ReferenceExternalTransactionID = "refund"
	rollback := newTransaction(t, r)
	if err := rollback.Process(w, wagering.Reference{Transaction: refund}, at); err != nil {
		t.Fatal(err)
	}
	if w.Balance().MinorUnits() != 7500 {
		t.Fatal("rollback did not reverse refund credit")
	}
	for _, kind := range []wagering.Kind{wagering.Refund, wagering.Rollback} {
		r = request(t, kind, 2500, "second"+string(kind))
		r.ReferenceExternalTransactionID = "bet"
		tx := newTransaction(t, r)
		if err := tx.Process(w, wagering.Reference{Transaction: bet, Compensated: true}, at); err != nil {
			t.Fatal(err)
		}
		if tx.FailureCode() != wagering.AlreadyReversed || w.Balance().MinorUnits() != 7500 {
			t.Fatal("original debit was compensated twice")
		}
	}
}

func TestProcessErrorsAreAtomic(t *testing.T) {
	for name, change := range map[string]func(*wallet.State){
		"wallet":  func(s *wallet.State) { s.ID = id("wrong") },
		"player":  func(s *wallet.State) { s.PlayerID = id("wrong") },
		"version": func(s *wallet.State) { s.Version = math.MaxInt64 },
		"time":    func(s *wallet.State) { s.UpdatedAt = at.Add(time.Hour) },
	} {
		t.Run(name, func(t *testing.T) {
			s := newWallet(t, 10000).Snapshot()
			change(&s)
			w, err := wallet.Rehydrate(s)
			if err != nil {
				t.Fatal(err)
			}
			tx := newTransaction(t, request(t, wagering.Bet, 100, "operation"))
			before := tx.Snapshot()
			walletBefore := w.Snapshot()
			if err := tx.Process(w, wagering.Reference{}, at); err == nil {
				t.Fatal("invalid processing accepted")
			}
			if tx.Snapshot() != before || w.Snapshot() != walletBefore {
				t.Fatal("processing error changed state")
			}
		})
	}
	tx := newTransaction(t, request(t, wagering.Refund, 100, "operation"))
	before := tx.Snapshot()
	w := newWallet(t, 10000)
	walletBefore := w.Snapshot()
	if err := tx.Process(w, wagering.Reference{Transaction: &wagering.WagerTransaction{}}, at); !errors.Is(err, wagering.ErrInvalidTransaction) {
		t.Fatal(err)
	}
	if err := tx.Process(nil, wagering.Reference{}, at); !errors.Is(err, wallet.ErrInvalidWallet) {
		t.Fatal(err)
	}
	if tx.Snapshot() != before || w.Snapshot() != walletBefore {
		t.Fatal("corrupt input changed state")
	}
	opening, err := wagering.NewOpening(id("opening"), w.ID(), w.PlayerID(), units(t, 1), at)
	if err != nil {
		t.Fatal(err)
	}
	if err := opening.Process(w, wagering.Reference{}, at); !errors.Is(err, wagering.ErrWalletScope) {
		t.Fatal(err)
	}
}

func TestWalletCurrencyMismatchIncludingLoss(t *testing.T) {
	for _, kind := range []wagering.Kind{wagering.Bet, wagering.Loss} {
		amount := int64(1)
		if kind == wagering.Loss {
			amount = 0
		}
		r := request(t, kind, amount, "operation")
		r.Money, _ = money.FromMinorUnits(amount, money.USD)
		tx := newTransaction(t, r)
		w := newWallet(t, 10000)
		before := w.Snapshot()
		if err := tx.Process(w, wagering.Reference{}, at); err != nil {
			t.Fatal(err)
		}
		if tx.FailureCode() != wagering.WalletCurrencyMismatch || w.Snapshot() != before {
			t.Fatal("wallet currency was ignored")
		}
	}
}
