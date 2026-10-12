package command

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/ports"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/events"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/ledger"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/money"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wagering"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wallet"
)

// Fakes record orchestration only: transaction semantics and SQL behaviour are
// covered by the integration suite against a real PostgreSQL instance.
var (
	errInjected = errors.New("injected failure")
	openingTime = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
)

type recorder struct{ calls []string }

func (r *recorder) record(call string) { r.calls = append(r.calls, call) }

type fakeUnitOfWork struct{ rec *recorder }

func (f *fakeUnitOfWork) WithinTx(_ context.Context, fn func(context.Context) error) error {
	f.rec.record("uow.begin")
	err := fn(context.Background())
	f.rec.record("uow.end")
	return err
}

type fakeWallets struct {
	rec    *recorder
	err    error
	states []wallet.State
}

func (f *fakeWallets) InsertWallet(_ context.Context, state wallet.State) error {
	f.rec.record("wallet.insert")
	if f.err != nil {
		return f.err
	}
	f.states = append(f.states, state)
	return nil
}

func (f *fakeWallets) LockWallet(context.Context, uuid.UUID) (wallet.State, error) {
	return wallet.State{}, errors.New("not used by create wallet")
}

func (f *fakeWallets) UpdateWalletBalance(context.Context, wallet.State, int64) error {
	return errors.New("not used by create wallet")
}

type fakeWagers struct {
	rec    *recorder
	err    error
	states []wagering.State
}

func (f *fakeWagers) InsertWagerTransaction(_ context.Context, state wagering.State) error {
	f.rec.record("wager.insert")
	if f.err != nil {
		return f.err
	}
	f.states = append(f.states, state)
	return nil
}

type fakeLedger struct {
	rec     *recorder
	err     error
	entries []ledger.Entry
}

func (f *fakeLedger) InsertLedgerEntry(_ context.Context, entry ledger.Entry) error {
	f.rec.record("ledger.insert")
	if f.err != nil {
		return f.err
	}
	f.entries = append(f.entries, entry)
	return nil
}

type fakeOutbox struct {
	rec       *recorder
	err       error
	envelopes []events.Envelope
}

func (f *fakeOutbox) InsertEvent(_ context.Context, envelope events.Envelope) error {
	f.rec.record("outbox.insert")
	if f.err != nil {
		return f.err
	}
	f.envelopes = append(f.envelopes, envelope)
	return nil
}

type fakes struct {
	uow     *fakeUnitOfWork
	wallets *fakeWallets
	wagers  *fakeWagers
	ledger  *fakeLedger
	outbox  *fakeOutbox
}

func newFakes() (*recorder, *fakes) {
	rec := &recorder{}
	return rec, &fakes{
		uow:     &fakeUnitOfWork{rec: rec},
		wallets: &fakeWallets{rec: rec},
		wagers:  &fakeWagers{rec: rec},
		ledger:  &fakeLedger{rec: rec},
		outbox:  &fakeOutbox{rec: rec},
	}
}

func (f *fakes) deps() CreateWalletDeps {
	return CreateWalletDeps{
		UnitOfWork: f.uow,
		Wallets:    f.wallets,
		Wagers:     f.wagers,
		Ledger:     f.ledger,
		Outbox:     f.outbox,
		Now:        func() time.Time { return openingTime },
		NewID:      uuid.New,
	}
}

func brl(cents int64) money.Money {
	value, err := money.FromMinorUnits(cents, money.BRL)
	if err != nil {
		panic(err)
	}
	return value
}

func validInput(balance money.Money) CreateWalletInput {
	return CreateWalletInput{PlayerID: uuid.New(), InitialBalance: balance, CorrelationID: uuid.New()}
}

func TestCreateWalletPositiveOpening(t *testing.T) {
	rec, f := newFakes()
	deps := f.deps()
	balance := brl(100000)

	result, err := deps.CreateWallet(context.Background(), validInput(balance))
	if err != nil {
		t.Fatalf("CreateWallet(): %v", err)
	}
	if result.WalletID == uuid.Nil || result.PlayerID == uuid.Nil || result.Balance != balance || result.Version != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}

	want := []string{
		"uow.begin", "wallet.insert", "wager.insert", "ledger.insert", "outbox.insert", "outbox.insert", "uow.end",
	}
	if len(rec.calls) != len(want) {
		t.Fatalf("call order: got %v, want %v", rec.calls, want)
	}
	for i, call := range rec.calls {
		if call != want[i] {
			t.Fatalf("call order: got %v, want %v", rec.calls, want)
		}
	}

	if len(f.wallets.states) != 1 {
		t.Fatalf("wallet states: %d", len(f.wallets.states))
	}
	state := f.wallets.states[0]
	if state.ID != result.WalletID || state.Version != 1 || state.Balance != balance || !state.CreatedAt.Equal(openingTime) {
		t.Fatalf("unexpected wallet state: %+v", state)
	}

	if len(f.wagers.states) != 1 {
		t.Fatalf("wager states: %d", len(f.wagers.states))
	}
	opening := f.wagers.states[0]
	if opening.Request.Kind != wagering.Opening || opening.Status != wagering.Processed {
		t.Fatalf("unexpected opening state: %+v", opening)
	}
	if !opening.HasResult || opening.Result != (wagering.Result{Balance: balance, WalletVersion: 1}) || opening.FailureCode != "" {
		t.Fatalf("unexpected opening result: %+v", opening)
	}
	if opening.Request.WalletID != result.WalletID || opening.Request.ID == uuid.Nil {
		t.Fatalf("unexpected opening identity: %+v", opening.Request)
	}

	if len(f.ledger.entries) != 1 {
		t.Fatalf("ledger entries: %d", len(f.ledger.entries))
	}
	entry := f.ledger.entries[0]
	zero := brl(0)
	if entry.Direction() != ledger.Credit || entry.Amount() != balance ||
		entry.BalanceBefore() != zero || entry.BalanceAfter() != balance ||
		entry.WalletID() != result.WalletID || entry.TransactionID() != opening.Request.ID {
		t.Fatalf("unexpected ledger entry: %+v", entry)
	}

	if len(f.outbox.envelopes) != 2 {
		t.Fatalf("outbox events: %d", len(f.outbox.envelopes))
	}
	processed := f.outbox.envelopes[0]
	if processed.EventType() != events.WagerTransactionProcessed || processed.Version() != 1 {
		t.Fatalf("unexpected processed event: %v", processed.EventType())
	}
	if processed.AggregateID() != opening.Request.ID {
		t.Fatalf("processed aggregate: %v", processed.AggregateID())
	}
	if causation, ok := processed.CausationID(); !ok || causation != opening.Request.ID {
		t.Fatalf("processed causation: (%v, %v)", causation, ok)
	}
	wantProcessed := events.WagerTransactionProcessedData{
		TransactionID: opening.Request.ID,
		WalletID:      result.WalletID,
		PlayerID:      result.PlayerID,
		Kind:          wagering.Opening,
		Money:         balance,
	}
	if got, ok := processed.Data().(events.WagerTransactionProcessedData); !ok || got != wantProcessed {
		t.Fatalf("processed data: %#v", processed.Data())
	}

	balanceChanged := f.outbox.envelopes[1]
	if balanceChanged.EventType() != events.WalletBalanceChanged || balanceChanged.Version() != 1 {
		t.Fatalf("unexpected balance event: %v", balanceChanged.EventType())
	}
	if balanceChanged.AggregateID() != result.WalletID {
		t.Fatalf("balance aggregate: %v", balanceChanged.AggregateID())
	}
	wantBalance := events.WalletBalanceChangedData{
		WalletID:      result.WalletID,
		TransactionID: opening.Request.ID,
		Direction:     ledger.Credit,
		Money:         balance,
		BalanceBefore: zero,
		BalanceAfter:  balance,
		WalletVersion: 1,
	}
	if got, ok := balanceChanged.Data().(events.WalletBalanceChangedData); !ok || got != wantBalance {
		t.Fatalf("balance data: %#v", balanceChanged.Data())
	}
	if !balanceChanged.OccurredAt().Equal(openingTime) {
		t.Fatalf("balance occurredAt: %v", balanceChanged.OccurredAt())
	}
}

func TestCreateWalletZeroCreatesOnlyTheWallet(t *testing.T) {
	rec, f := newFakes()
	deps := f.deps()

	result, err := deps.CreateWallet(context.Background(), validInput(brl(0)))
	if err != nil {
		t.Fatalf("CreateWallet(): %v", err)
	}
	if result.Version != 1 || result.Balance.MinorUnits() != 0 {
		t.Fatalf("unexpected result: %+v", result)
	}
	want := []string{"uow.begin", "wallet.insert", "uow.end"}
	if len(rec.calls) != len(want) {
		t.Fatalf("call order: got %v, want %v", rec.calls, want)
	}
	for i, call := range rec.calls {
		if call != want[i] {
			t.Fatalf("call order: got %v, want %v", rec.calls, want)
		}
	}
	if len(f.wagers.states) != 0 || len(f.ledger.entries) != 0 || len(f.outbox.envelopes) != 0 {
		t.Fatal("zero opening must not create transactions, ledger entries or events")
	}
}

func TestCreateWalletConflictStopsBeforeFinancialWrites(t *testing.T) {
	rec, f := newFakes()
	f.wallets.err = ports.ErrWalletAlreadyExists
	deps := f.deps()

	_, err := deps.CreateWallet(context.Background(), validInput(brl(100000)))
	if !errors.Is(err, ports.ErrWalletAlreadyExists) {
		t.Fatalf("CreateWallet(): got %v, want ErrWalletAlreadyExists", err)
	}
	for _, call := range rec.calls {
		if call == "wager.insert" || call == "ledger.insert" || call == "outbox.insert" {
			t.Fatalf("financial writes must not run after a wallet conflict: %v", rec.calls)
		}
	}
}

func TestCreateWalletLaterFailurePropagates(t *testing.T) {
	_, f := newFakes()
	f.ledger.err = errInjected
	deps := f.deps()

	_, err := deps.CreateWallet(context.Background(), validInput(brl(100000)))
	if !errors.Is(err, errInjected) {
		t.Fatalf("CreateWallet(): got %v, want the injected failure", err)
	}
	if len(f.outbox.envelopes) != 0 {
		t.Fatal("no event may be recorded after a failed ledger write")
	}
}

func TestCreateWalletRejectsInvalidInput(t *testing.T) {
	usd, err := money.FromMinorUnits(100, money.USD)
	if err != nil {
		t.Fatal(err)
	}
	negative := brl(-100)
	cases := []struct {
		name    string
		mutate  func(in *CreateWalletInput)
		wantErr error
	}{
		{"nil player id", func(in *CreateWalletInput) { in.PlayerID = uuid.Nil }, ErrInvalidPlayerID},
		{"nil correlation id", func(in *CreateWalletInput) { in.CorrelationID = uuid.Nil }, ErrInvalidCorrelation},
		{"uninitialized money", func(in *CreateWalletInput) { in.InitialBalance = money.Money{} }, ErrInvalidAmount},
		{"negative amount", func(in *CreateWalletInput) { in.InitialBalance = negative }, ErrInvalidAmount},
		{"currency not accepted", func(in *CreateWalletInput) { in.InitialBalance = usd }, ErrCurrencyNotAccepted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, f := newFakes()
			deps := f.deps()
			input := validInput(brl(100))
			tc.mutate(&input)

			result, err := deps.CreateWallet(context.Background(), input)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("CreateWallet(): got %v, want %v", err, tc.wantErr)
			}
			if result != (CreateWalletResult{}) {
				t.Fatalf("rejected input must return an empty result: %+v", result)
			}
			if len(rec.calls) != 0 {
				t.Fatalf("rejected input must not touch any port: %v", rec.calls)
			}
		})
	}
}

func TestCreateWalletRejectsIncompleteDeps(t *testing.T) {
	_, f := newFakes()
	deps := f.deps()
	deps.Now = nil

	if _, err := deps.CreateWallet(context.Background(), validInput(brl(100))); !errors.Is(err, ErrIncompleteDeps) {
		t.Fatalf("CreateWallet(): got %v, want ErrIncompleteDeps", err)
	}
}
