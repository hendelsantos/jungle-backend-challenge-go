package wallet_test

import (
	"errors"
	"math"
	"math/big"
	"testing"
	"time"

	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/money"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wallet"
)

func FuzzMovementSequence(f *testing.F) {
	f.Add(uint64(10000), []byte{80, 80, 255, 0})
	f.Add(uint64(0), []byte{1, 255, 1})
	f.Add(uint64(math.MaxInt64), []byte{129, 1, 129})
	f.Fuzz(func(t *testing.T, initial uint64, operations []byte) {
		if len(operations) > 256 {
			operations = operations[:256]
		}
		w := newWallet(t, int64(initial&math.MaxInt64))
		for i, operation := range operations {
			before := w.Snapshot()
			amount := int64(operation&127) * 100
			at := created.Add(time.Duration(i+1) * time.Second)
			want := big.NewInt(before.Balance.MinorUnits())
			var err error
			if operation&128 == 0 {
				want.Sub(want, big.NewInt(amount))
				err = w.Debit(units(t, amount, money.BRL), at)
			} else {
				want.Add(want, big.NewInt(amount))
				err = w.Credit(units(t, amount, money.BRL), at)
			}
			var wantErr error
			if want.Sign() < 0 {
				wantErr = wallet.ErrInsufficientFunds
			} else if !want.IsInt64() {
				wantErr = money.ErrOverflow
			}
			if !errors.Is(err, wantErr) {
				t.Fatalf("step %d: error = %v, want %v", i, err, wantErr)
			}
			if err != nil || amount == 0 {
				if w.Snapshot() != before {
					t.Fatal("rejection or zero movement changed state")
				}
			} else if w.Balance().MinorUnits() != want.Int64() || w.Version() != before.Version+1 || w.UpdatedAt() != at.UTC() {
				t.Fatalf("step %d: wrong state %+v", i, w.Snapshot())
			}
			if err := w.Validate(); err != nil {
				t.Fatal(err)
			}
		}
	})
}
