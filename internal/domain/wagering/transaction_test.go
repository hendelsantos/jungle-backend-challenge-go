package wagering_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/money"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wagering"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wallet"
)

var at = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

func TestExternalConstructionAndAmounts(t *testing.T) {
	for _, kind := range []wagering.Kind{wagering.Bet, wagering.Win, wagering.Loss, wagering.Refund, wagering.Rollback} {
		for _, amount := range []int64{-1, 0, 1} {
			r := request(t, kind, amount, "operation")
			tx, err := wagering.NewExternal(r, at)
			valid := amount > 0 && kind != wagering.Loss || amount == 0 && kind == wagering.Loss
			if valid {
				if err != nil {
					t.Fatal(err)
				}
				if tx.Snapshot().Request != r || tx.Status() != wagering.Pending {
					t.Fatal("construction changed request")
				}
			} else if tx != nil || !errors.Is(err, wagering.ErrInvalidAmount) {
				t.Fatalf("%s amount %d: %v", kind, amount, err)
			}
		}
	}
	if _, err := wagering.NewExternal(request(t, wagering.Opening, 1, "opening"), at); !errors.Is(err, wagering.ErrInvalidKind) {
		t.Fatal(err)
	}
	if _, err := wagering.NewExternal(request(t, "UNKNOWN", 1, "unknown"), at); !errors.Is(err, wagering.ErrInvalidKind) {
		t.Fatal(err)
	}
}

func TestRequestValidation(t *testing.T) {
	for name, change := range map[string]func(*wagering.Request){
		"id":                   func(r *wagering.Request) { r.ID = uuid.Nil },
		"wallet":               func(r *wagering.Request) { r.WalletID = uuid.Nil },
		"player":               func(r *wagering.Request) { r.PlayerID = uuid.Nil },
		"provider":             func(r *wagering.Request) { r.ProviderID = " " },
		"external":             func(r *wagering.Request) { r.ExternalTransactionID = "" },
		"key":                  func(r *wagering.Request) { r.IdempotencyKey = "" },
		"round":                func(r *wagering.Request) { r.RoundID = "" },
		"game":                 func(r *wagering.Request) { r.GameID = "" },
		"hash length":          func(r *wagering.Request) { r.PayloadHash = "ab" },
		"hash hex":             func(r *wagering.Request) { r.PayloadHash = strings.Repeat("z", 64) },
		"hash case":            func(r *wagering.Request) { r.PayloadHash = strings.Repeat("AB", 32) },
		"money":                func(r *wagering.Request) { r.Money = money.Money{} },
		"unexpected reference": func(r *wagering.Request) { r.ReferenceExternalTransactionID = "bet" },
	} {
		t.Run(name, func(t *testing.T) {
			r := request(t, wagering.Bet, 1, "tx")
			change(&r)
			if tx, err := wagering.NewExternal(r, at); err == nil || tx != nil {
				t.Fatalf("accepted invalid request: %v", tx)
			}
		})
	}
	for _, kind := range []wagering.Kind{wagering.Refund, wagering.Rollback, wagering.Win} {
		for _, ref := range []string{" ", "tx", ""} {
			if kind == wagering.Win && ref == "" {
				continue
			}
			r := request(t, kind, 1, "tx")
			r.ReferenceExternalTransactionID = ref
			if _, err := wagering.NewExternal(r, at); !errors.Is(err, wagering.ErrInvalidReference) {
				t.Fatalf("%s ref %q: %v", kind, ref, err)
			}
		}
	}
	if _, err := wagering.NewExternal(request(t, wagering.Bet, 1, "tx"), time.Time{}); !errors.Is(err, wagering.ErrInvalidTime) {
		t.Fatal(err)
	}
}

func TestOpeningMetadataAndRehydration(t *testing.T) {
	for _, amount := range []int64{0, 10000} {
		w := newWallet(t, amount)
		tx, err := wagering.NewOpening(id("opening"), w.ID(), w.PlayerID(), units(t, amount), at)
		if err != nil {
			t.Fatal(err)
		}
		before := w.Snapshot()
		if err := tx.Process(w, wagering.Reference{}, at); err != nil {
			t.Fatal(err)
		}
		if w.Snapshot() != before || tx.Status() != wagering.Processed {
			t.Fatal("opening credited the wallet again")
		}
		result, ok := tx.Result()
		if !ok || result.Balance != w.Balance() || result.WalletVersion != 1 {
			t.Fatal("incorrect opening result")
		}
		snapshot := tx.Snapshot()
		for _, field := range []string{snapshot.Request.ProviderID, snapshot.Request.ExternalTransactionID, snapshot.Request.IdempotencyKey, snapshot.Request.PayloadHash, snapshot.Request.RoundID, snapshot.Request.GameID, snapshot.Request.ReferenceExternalTransactionID} {
			if field != "" {
				t.Fatal("opening contains external metadata")
			}
		}
		restored, err := wagering.Rehydrate(snapshot)
		if err != nil || restored.Snapshot() != snapshot {
			t.Fatalf("rehydration changed state: %v", err)
		}
		if err := restored.Process(w, wagering.Reference{}, at); !errors.Is(err, wagering.ErrInvalidTransition) {
			t.Fatal(err)
		}
		if w.Snapshot() != before {
			t.Fatal("opening replay changed wallet")
		}
		snapshot.Request.ProviderID = "provider"
		if _, err := wagering.Rehydrate(snapshot); err == nil {
			t.Fatal("accepted external metadata in opening")
		}
	}
	if _, err := wagering.NewOpening(id("opening"), id("wallet"), id("player"), units(t, -1), at); !errors.Is(err, wagering.ErrInvalidAmount) {
		t.Fatal(err)
	}
}

func TestTerminalStatesAreImmutable(t *testing.T) {
	for _, terminal := range []wagering.Status{wagering.Processed, wagering.Rejected, wagering.Failed} {
		for _, startPendingReference := range []bool{false, true} {
			tx := newTransaction(t, request(t, wagering.Win, 100, "win"))
			if startPendingReference {
				r := request(t, wagering.Win, 100, "win")
				r.ReferenceExternalTransactionID = "reference"
				tx = newTransaction(t, r)
				if err := tx.Process(newWallet(t, 10000), wagering.Reference{}, at); err != nil {
					t.Fatal(err)
				}
			}
			w := newWallet(t, 10000)
			switch terminal {
			case wagering.Processed:
				if err := tx.Process(w, wagering.Reference{Transaction: reference(t, wagering.Bet, 100)}, at); err != nil {
					t.Fatal(err)
				}
			case wagering.Rejected:
				if err := tx.Reject(wagering.ReferenceNotFound, at); err != nil {
					t.Fatal(err)
				}
			case wagering.Failed:
				if err := tx.FailPermanent(at); err != nil {
					t.Fatal(err)
				}
			}
			if tx.Status() != terminal {
				t.Fatalf("status = %s, want %s", tx.Status(), terminal)
			}
			before, walletBefore := tx.Snapshot(), w.Snapshot()
			for _, err := range []error{tx.Process(w, wagering.Reference{}, at), tx.Reject(wagering.AlreadyReversed, at), tx.FailPermanent(at)} {
				if !errors.Is(err, wagering.ErrInvalidTransition) {
					t.Fatalf("terminal allowed transition: %v", err)
				}
			}
			if tx.Snapshot() != before || w.Snapshot() != walletBefore {
				t.Fatal("terminal transition changed state")
			}
		}
	}
}

func TestInvalidRehydration(t *testing.T) {
	base := newTransaction(t, request(t, wagering.Win, 100, "win")).Snapshot()
	for name, change := range map[string]func(*wagering.State){
		"status":                 func(s *wagering.State) { s.Status = "unknown" },
		"time":                   func(s *wagering.State) { s.UpdatedAt = at.Add(-time.Second) },
		"failure on pending":     func(s *wagering.State) { s.FailureCode = wagering.AlreadyReversed },
		"reference on pending":   func(s *wagering.State) { s.ReferenceID = id("reference") },
		"wait without reference": func(s *wagering.State) { s.Status = wagering.PendingReference },
		"processed no result":    func(s *wagering.State) { s.Status = wagering.Processed },
		"rejected no code":       func(s *wagering.State) { s.Status = wagering.Rejected },
		"failed wrong code":      func(s *wagering.State) { s.Status = wagering.Failed; s.FailureCode = wagering.InsufficientFunds },
		"hidden result":          func(s *wagering.State) { s.Result = wagering.Result{Balance: units(t, 0), WalletVersion: 1} },
		"pending result": func(s *wagering.State) {
			s.HasResult = true
			s.Result = wagering.Result{Balance: units(t, 0), WalletVersion: 1}
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := base
			change(&s)
			if restored, err := wagering.Rehydrate(s); err == nil || restored != nil {
				t.Fatal("accepted inconsistent state")
			}
		})
	}
	processed := reference(t, wagering.Bet, 100).Snapshot()
	for _, change := range []func(*wagering.State){
		func(s *wagering.State) { s.Result.Balance = money.Money{} },
		func(s *wagering.State) { s.Result.Balance = units(t, -1) },
		func(s *wagering.State) { s.Result.WalletVersion = 0 },
		func(s *wagering.State) { s.Result.Balance, _ = money.Zero(money.USD) },
		func(s *wagering.State) { s.FailureCode = wagering.InsufficientFunds },
		func(s *wagering.State) {
			s.Request.ReferenceExternalTransactionID = "missing"
			s.Request.Kind = wagering.Win
		},
	} {
		s := processed
		change(&s)
		if _, err := wagering.Rehydrate(s); err == nil {
			t.Fatal("accepted invalid processed state")
		}
	}
}

func TestInvalidReceiversAndTransitions(t *testing.T) {
	for _, tx := range []*wagering.WagerTransaction{nil, {}} {
		for _, err := range []error{tx.Validate(), tx.Process(newWallet(t, 100), wagering.Reference{}, at), tx.Reject(wagering.AlreadyReversed, at), tx.FailPermanent(at)} {
			if !errors.Is(err, wagering.ErrInvalidTransaction) {
				t.Fatal(err)
			}
		}
	}
	tx := newTransaction(t, request(t, wagering.Bet, 1, "tx"))
	before := tx.Snapshot()
	if err := tx.Reject("unknown", at); !errors.Is(err, wagering.ErrInvalidTransition) {
		t.Fatal(err)
	}
	for _, when := range []time.Time{{}, at.Add(-time.Second)} {
		for _, err := range []error{tx.Reject(wagering.InsufficientFunds, when), tx.FailPermanent(when), tx.Process(newWallet(t, 100), wagering.Reference{}, when)} {
			if !errors.Is(err, wagering.ErrInvalidTime) {
				t.Fatal(err)
			}
		}
	}
	if tx.Snapshot() != before {
		t.Fatal("invalid transition changed state")
	}
}

func id(value string) uuid.UUID { return uuid.NewSHA1(uuid.NameSpaceURL, []byte(value)) }
func units(t testing.TB, value int64) money.Money {
	t.Helper()
	m, err := money.FromMinorUnits(value, money.BRL)
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func request(t testing.TB, kind wagering.Kind, amount int64, external string) wagering.Request {
	t.Helper()
	r := wagering.Request{ID: id(external), WalletID: id("wallet"), PlayerID: id("player"), Kind: kind, Money: units(t, amount), ProviderID: "provider-a", ExternalTransactionID: external, IdempotencyKey: "key:" + external, PayloadHash: strings.Repeat("ab", 32), RoundID: "round", GameID: "game"}
	if kind == wagering.Refund || kind == wagering.Rollback {
		r.ReferenceExternalTransactionID = "reference"
	}
	return r
}
func newTransaction(t testing.TB, r wagering.Request) *wagering.WagerTransaction {
	t.Helper()
	tx, err := wagering.NewExternal(r, at)
	if err != nil {
		t.Fatal(err)
	}
	return tx
}
func newWallet(t testing.TB, balance int64) *wallet.Wallet {
	t.Helper()
	w, err := wallet.New(id("wallet"), id("player"), units(t, balance), at)
	if err != nil {
		t.Fatal(err)
	}
	return w
}
func reference(t testing.TB, kind wagering.Kind, amount int64) *wagering.WagerTransaction {
	t.Helper()
	r := request(t, kind, amount, "reference")
	s := wagering.State{Request: r, Status: wagering.Processed, CreatedAt: at, UpdatedAt: at, HasResult: true, Result: wagering.Result{Balance: units(t, 10000), WalletVersion: 2}}
	if kind == wagering.Refund || kind == wagering.Rollback {
		s.Request.ReferenceExternalTransactionID = "original"
		s.ReferenceID = id("original")
	}
	tx, err := wagering.Rehydrate(s)
	if err != nil {
		t.Fatal(err)
	}
	return tx
}
