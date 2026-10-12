package query

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/ports"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wallet"
)

type fakeWalletReader struct {
	state wallet.State
	err   error
}

func (f *fakeWalletReader) FindWalletByID(context.Context, uuid.UUID) (wallet.State, error) {
	return f.state, f.err
}

type fakeLedgerReader struct {
	gotWalletID uuid.UUID
	gotCursor   string
	gotLimit    int
	called      bool
	page        ports.LedgerPage
	err         error
}

func (f *fakeLedgerReader) ListEntriesByWallet(_ context.Context, walletID uuid.UUID, cursor string, limit int) (ports.LedgerPage, error) {
	f.called = true
	f.gotWalletID, f.gotCursor, f.gotLimit = walletID, cursor, limit
	return f.page, f.err
}

type fakeReconciliation struct {
	report ports.ReconciliationReport
	err    error
}

func (f *fakeReconciliation) ReconcileWallet(context.Context, uuid.UUID) (ports.ReconciliationReport, error) {
	return f.report, f.err
}

func TestGetWallet(t *testing.T) {
	t.Run("returns the reader state", func(t *testing.T) {
		state := wallet.State{ID: uuid.New(), Version: 1}
		deps := GetWalletDeps{Wallets: &fakeWalletReader{state: state}}
		got, err := deps.GetWallet(context.Background(), state.ID)
		if err != nil || got != state {
			t.Fatalf("GetWallet(): (%+v, %v)", got, err)
		}
	})
	t.Run("propagates not found", func(t *testing.T) {
		deps := GetWalletDeps{Wallets: &fakeWalletReader{err: ports.ErrWalletNotFound}}
		if _, err := deps.GetWallet(context.Background(), uuid.New()); !errors.Is(err, ports.ErrWalletNotFound) {
			t.Fatalf("GetWallet(): got %v", err)
		}
	})
	t.Run("rejects nil wallet id", func(t *testing.T) {
		deps := GetWalletDeps{Wallets: &fakeWalletReader{}}
		if _, err := deps.GetWallet(context.Background(), uuid.Nil); !errors.Is(err, ErrInvalidWalletID) {
			t.Fatalf("GetWallet(): got %v", err)
		}
	})
	t.Run("rejects incomplete deps", func(t *testing.T) {
		if _, err := (GetWalletDeps{}).GetWallet(context.Background(), uuid.New()); !errors.Is(err, ErrIncompleteDeps) {
			t.Fatalf("GetWallet(): got %v", err)
		}
	})
}

func TestListLedger(t *testing.T) {
	page := ports.LedgerPage{NextCursor: "next"}
	base := ListLedgerDeps{
		Wallets: &fakeWalletReader{state: wallet.State{ID: uuid.New(), Version: 1}},
		Ledger:  &fakeLedgerReader{page: page},
	}
	newDeps := func() (ListLedgerDeps, *fakeLedgerReader) {
		deps := base
		reader := &fakeLedgerReader{page: page}
		deps.Ledger = reader
		return deps, reader
	}

	t.Run("zero limit becomes the default", func(t *testing.T) {
		deps, reader := newDeps()
		got, err := deps.ListLedger(context.Background(), uuid.New(), "", 0)
		if err != nil || got.NextCursor != "next" || reader.gotLimit != LedgerDefaultLimit {
			t.Fatalf("ListLedger(): (%+v, %v), limit %d", got, err, reader.gotLimit)
		}
	})
	t.Run("explicit limit passes through", func(t *testing.T) {
		deps, reader := newDeps()
		if _, err := deps.ListLedger(context.Background(), uuid.New(), "cursor", 25); err != nil {
			t.Fatal(err)
		}
		if reader.gotLimit != 25 || reader.gotCursor != "cursor" {
			t.Fatalf("reader called with limit %d, cursor %q", reader.gotLimit, reader.gotCursor)
		}
	})
	t.Run("rejects limits out of bounds", func(t *testing.T) {
		for _, limit := range []int{-1, LedgerMaxLimit + 1} {
			deps, _ := newDeps()
			if _, err := deps.ListLedger(context.Background(), uuid.New(), "", limit); !errors.Is(err, ErrInvalidLimit) {
				t.Fatalf("ListLedger(limit=%d): got %v", limit, err)
			}
		}
	})
	t.Run("missing wallet stops before the ledger read", func(t *testing.T) {
		deps := ListLedgerDeps{
			Wallets: &fakeWalletReader{err: ports.ErrWalletNotFound},
			Ledger:  &fakeLedgerReader{},
		}
		if _, err := deps.ListLedger(context.Background(), uuid.New(), "", 10); !errors.Is(err, ports.ErrWalletNotFound) {
			t.Fatalf("ListLedger(): got %v", err)
		}
		if deps.Ledger.(*fakeLedgerReader).called {
			t.Fatal("ledger reader must not be called for a missing wallet")
		}
	})
	t.Run("rejects nil wallet id and incomplete deps", func(t *testing.T) {
		deps, _ := newDeps()
		if _, err := deps.ListLedger(context.Background(), uuid.Nil, "", 10); !errors.Is(err, ErrInvalidWalletID) {
			t.Fatalf("got %v", err)
		}
		if _, err := (ListLedgerDeps{}).ListLedger(context.Background(), uuid.New(), "", 10); !errors.Is(err, ErrIncompleteDeps) {
			t.Fatalf("got %v", err)
		}
	})
}

func TestReconcileWallet(t *testing.T) {
	t.Run("returns the report", func(t *testing.T) {
		report := ports.ReconciliationReport{WalletID: uuid.New(), Consistent: true}
		deps := ReconcileWalletDeps{Reconciliation: &fakeReconciliation{report: report}}
		got, err := deps.ReconcileWallet(context.Background(), report.WalletID)
		if err != nil || got != report {
			t.Fatalf("ReconcileWallet(): (%+v, %v)", got, err)
		}
	})
	t.Run("propagates errors", func(t *testing.T) {
		deps := ReconcileWalletDeps{Reconciliation: &fakeReconciliation{err: ports.ErrWalletNotFound}}
		if _, err := deps.ReconcileWallet(context.Background(), uuid.New()); !errors.Is(err, ports.ErrWalletNotFound) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("rejects nil wallet id and incomplete deps", func(t *testing.T) {
		deps := ReconcileWalletDeps{Reconciliation: &fakeReconciliation{}}
		if _, err := deps.ReconcileWallet(context.Background(), uuid.Nil); !errors.Is(err, ErrInvalidWalletID) {
			t.Fatalf("got %v", err)
		}
		if _, err := (ReconcileWalletDeps{}).ReconcileWallet(context.Background(), uuid.New()); !errors.Is(err, ErrIncompleteDeps) {
			t.Fatalf("got %v", err)
		}
	})
}
