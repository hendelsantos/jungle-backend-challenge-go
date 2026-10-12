package wagering_test

import (
	"errors"
	"math"
	"testing"

	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wagering"
)

func FuzzBetRefundRollback(f *testing.F) {
	f.Add(uint64(10000), uint64(2500))
	f.Add(uint64(0), uint64(1))
	f.Add(uint64(math.MaxInt64), uint64(math.MaxInt64))
	f.Fuzz(func(t *testing.T, initial, amount uint64) {
		initial &= math.MaxInt64
		amount &= math.MaxInt64
		if amount == 0 {
			amount = 1
		}
		w := newWallet(t, int64(initial))
		bet := newTransaction(t, request(t, wagering.Bet, int64(amount), "bet"))
		if err := bet.Process(w, wagering.Reference{}, at); err != nil {
			t.Fatal(err)
		}
		if amount > initial {
			if bet.FailureCode() != wagering.InsufficientFunds || w.Balance().MinorUnits() != int64(initial) || w.Version() != 1 {
				t.Fatal("insufficient BET changed financial state")
			}
			return
		}
		afterBet := w.Balance()
		r := request(t, wagering.Refund, int64(amount), "refund")
		r.ReferenceExternalTransactionID = "bet"
		refund := newTransaction(t, r)
		if err := refund.Process(w, wagering.Reference{Transaction: bet}, at); err != nil {
			t.Fatal(err)
		}
		if refund.Status() != wagering.Processed || w.Balance().MinorUnits() != int64(initial) || w.Version() != 3 {
			t.Fatal("REFUND did not restore the original balance exactly")
		}
		r = request(t, wagering.Rollback, int64(amount), "rollback")
		r.ReferenceExternalTransactionID = "refund"
		rollback := newTransaction(t, r)
		if err := rollback.Process(w, wagering.Reference{Transaction: refund}, at); err != nil {
			t.Fatal(err)
		}
		if rollback.Status() != wagering.Processed || w.Balance() != afterBet || w.Version() != 4 {
			t.Fatal("ROLLBACK did not restore the post-BET balance exactly")
		}
		walletBefore := w.Snapshot()
		for _, tx := range []*wagering.WagerTransaction{bet, refund, rollback} {
			before := tx.Snapshot()
			if err := tx.Process(w, wagering.Reference{}, at); !errors.Is(err, wagering.ErrInvalidTransition) {
				t.Fatal(err)
			}
			if tx.Snapshot() != before || w.Snapshot() != walletBefore {
				t.Fatal("terminal replay changed state")
			}
		}
	})
}
