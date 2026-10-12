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

var retryTime = time.Date(2026, 10, 12, 12, 30, 0, 0, time.UTC)

// pendingState builds a valid suspended REFUND waiting for a BET reference.
func pendingState(walletID, playerID uuid.UUID, updatedAt time.Time) wagering.State {
	transaction, err := wagering.NewExternal(wagering.Request{
		ID:                             uuid.New(),
		WalletID:                       walletID,
		PlayerID:                       playerID,
		Kind:                           wagering.Refund,
		Money:                          mustMoney("25.00"),
		ProviderID:                     "provider-a",
		ExternalTransactionID:          "refund-1",
		IdempotencyKey:                 "provider-a:refund-1",
		PayloadHash:                    otherHash,
		RoundID:                        "round-1",
		GameID:                         "fortune-chimp",
		ReferenceExternalTransactionID: "bet-1",
	}, updatedAt)
	if err != nil {
		panic(err)
	}
	if err := transaction.Process(walletAt(walletID, playerID, "75.00", updatedAt), wagering.Reference{}, updatedAt); err != nil {
		panic(err)
	}
	return transaction.Snapshot()
}

// walletAt builds a locked wallet snapshot for the fake repository.
func walletAt(walletID, playerID uuid.UUID, balance string, at time.Time) *wallet.Wallet {
	value, err := money.ParseDecimal(balance, money.BRL)
	if err != nil {
		panic(err)
	}
	w, err := wallet.New(walletID, playerID, value, at)
	if err != nil {
		panic(err)
	}
	return w
}

func mustMoney(amount string) money.Money {
	value, err := money.ParseDecimal(amount, money.BRL)
	if err != nil {
		panic(err)
	}
	return value
}

// --- retry fakes -------------------------------------------------------------

type retryClaimer struct {
	due  []ports.DuePendingReference
	seen int
}

func (f *retryClaimer) ClaimDuePendingReferences(context.Context, time.Time, int) ([]ports.DuePendingReference, error) {
	f.seen++
	if f.seen == 1 {
		return f.due, nil
	}
	return nil, nil
}

type retryWagers struct {
	inserted  []wagering.State
	updated   []wagering.State
	schedules []ports.ReferenceSchedule
}

func (f *retryWagers) InsertWagerTransaction(_ context.Context, state wagering.State) error {
	f.inserted = append(f.inserted, state)
	return nil
}

func (f *retryWagers) UpdateWagerTransaction(_ context.Context, state wagering.State, schedule ports.ReferenceSchedule) error {
	f.updated = append(f.updated, state)
	f.schedules = append(f.schedules, schedule)
	return nil
}

type retryWallets struct {
	locked  wallet.State
	updates []wallet.State
	locks   int
}

func (f *retryWallets) InsertWallet(context.Context, wallet.State) error { return errors.New("unused") }

func (f *retryWallets) LockWallet(_ context.Context, id uuid.UUID) (wallet.State, error) {
	f.locks++
	if f.locked.ID == uuid.Nil {
		f.locked.ID = id
	}
	return f.locked, nil
}

func (f *retryWallets) UpdateWalletBalance(_ context.Context, state wallet.State, _ int64) error {
	f.updates = append(f.updates, state)
	return nil
}

type retryLedger struct{ entries int }

func (f *retryLedger) InsertLedgerEntry(context.Context, ledger.Entry) error {
	f.entries++
	return nil
}

type retryOutbox struct{ envelopes []events.Envelope }

func (f *retryOutbox) InsertEvent(_ context.Context, envelope events.Envelope) error {
	f.envelopes = append(f.envelopes, envelope)
	return nil
}

type retryDeps struct {
	uow     *recordingUoW
	wallets *retryWallets
	wagers  *retryWagers
	lookups *processLookups
	claimer *retryClaimer
	ledger  *retryLedger
	outbox  *retryOutbox
}

func newRetryDeps(walletID, playerID uuid.UUID, due ports.DuePendingReference) *retryDeps {
	lockedWallet := walletAt(walletID, playerID, "75.00", retryTime).Snapshot()
	return &retryDeps{
		uow:     &recordingUoW{},
		wallets: &retryWallets{locked: lockedWallet},
		wagers:  &retryWagers{},
		lookups: &processLookups{},
		claimer: &retryClaimer{due: []ports.DuePendingReference{due}},
		ledger:  &retryLedger{},
		outbox:  &retryOutbox{},
	}
}

func (f *retryDeps) deps() RetryReferenceDeps {
	return RetryReferenceDeps{
		UnitOfWork: f.uow, Wallets: f.wallets, Wagers: f.wagers, Lookups: f.lookups,
		Pending: f.claimer, Ledger: f.ledger, Outbox: f.outbox,
		Now: func() time.Time { return retryTime }, NewID: uuid.New,
		ReferenceBackoff: func(attempt int) time.Duration { return time.Duration(attempt) * time.Second },
	}
}

func TestRetryReferenceRequiresDeps(t *testing.T) {
	if err := (RetryReferenceDeps{Now: func() time.Time { return retryTime }, NewID: uuid.New}).validate(); !errors.Is(err, ErrIncompleteDeps) {
		t.Fatalf("err = %v, want ErrIncompleteDeps", err)
	}
}

func TestRetryReferenceRejectsInvalidLimit(t *testing.T) {
	f := newRetryDeps(uuid.New(), uuid.New(), ports.DuePendingReference{})
	if _, err := f.deps().RetryDueReferences(context.Background(), 0); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

func TestRetryReferenceNoDueRowsIsNoop(t *testing.T) {
	f := newRetryDeps(uuid.New(), uuid.New(), ports.DuePendingReference{})
	f.claimer.due = nil
	processed, err := f.deps().RetryDueReferences(context.Background(), 10)
	if err != nil || processed != 0 {
		t.Fatalf("processed=%d err=%v", processed, err)
	}
	if len(f.wagers.updated) != 0 || f.outbox.envelopes != nil || f.wallets.locks != 0 {
		t.Fatal("no-op batch touched state")
	}
}

func TestRetryReferenceReschedulesStillMissingReference(t *testing.T) {
	walletID, playerID := uuid.New(), uuid.New()
	due := ports.DuePendingReference{State: pendingState(walletID, playerID, retryTime)}
	f := newRetryDeps(walletID, playerID, due)
	processed, err := f.deps().RetryDueReferences(context.Background(), 10)
	if err != nil || processed != 1 {
		t.Fatalf("processed=%d err=%v", processed, err)
	}
	if len(f.wagers.updated) != 1 || f.wagers.updated[0].Status != wagering.PendingReference {
		t.Fatalf("updated = %+v", f.wagers.updated)
	}
	schedule := f.wagers.schedules[0]
	if schedule.Attempts != 1 || !schedule.NextAttemptAt.Equal(retryTime.Add(time.Second)) || !schedule.HasNextAttempt {
		t.Fatalf("schedule = %+v", schedule)
	}
	if !schedule.HasDeadline || !schedule.DeadlineAt.Equal(due.State.CreatedAt.Add(DefaultReferenceTTL)) {
		t.Fatalf("deadline = %v, want created_at + default TTL", schedule.DeadlineAt)
	}
	if f.outbox.envelopes != nil || f.wallets.updates != nil {
		t.Fatal("reschedule produced events or balance writes")
	}
}

func TestRetryReferenceResolvesArrivedReference(t *testing.T) {
	walletID, playerID := uuid.New(), uuid.New()
	pending := pendingState(walletID, playerID, retryTime)
	f := newRetryDeps(walletID, playerID, ports.DuePendingReference{State: pending})
	// The referenced BET arrived and was processed in the meantime.
	bet, err := wagering.NewExternal(wagering.Request{
		ID: uuid.New(), WalletID: walletID, PlayerID: playerID, Kind: wagering.Bet,
		Money: mustMoney("25.00"), ProviderID: "provider-a", ExternalTransactionID: "bet-1",
		IdempotencyKey: "provider-a:bet-1", PayloadHash: otherHash,
		RoundID: "round-1", GameID: "fortune-chimp",
	}, retryTime)
	if err != nil {
		t.Fatal(err)
	}
	betWallet := walletAt(walletID, playerID, "75.00", retryTime)
	if err := bet.Process(betWallet, wagering.Reference{}, retryTime); err != nil {
		t.Fatal(err)
	}
	f.lookups.hasReference, f.lookups.reference = true, ports.ResolvedReference{State: bet.Snapshot()}

	processed, err := f.deps().RetryDueReferences(context.Background(), 10)
	if err != nil || processed != 1 {
		t.Fatalf("processed=%d err=%v", processed, err)
	}
	updated := f.wagers.updated[0]
	if updated.Status != wagering.Processed || !updated.HasResult || updated.Result.Balance.MinorUnits() != 10000 {
		t.Fatalf("updated = %+v", updated)
	}
	if updated.ReferenceID != bet.ID() {
		t.Fatalf("reference id = %v, want %v", updated.ReferenceID, bet.ID())
	}
	if len(f.wallets.updates) != 1 || f.wallets.updates[0].Balance.MinorUnits() != 10000 {
		t.Fatalf("balance updates = %+v", f.wallets.updates)
	}
	if f.ledger.entries != 1 {
		t.Fatalf("ledger entries = %d", f.ledger.entries)
	}
	if len(f.outbox.envelopes) != 2 {
		t.Fatalf("envelopes = %d, want processed + balance changed", len(f.outbox.envelopes))
	}
	if f.outbox.envelopes[0].EventType() != events.WagerTransactionProcessed ||
		f.outbox.envelopes[1].EventType() != events.WalletBalanceChanged {
		t.Fatalf("event types = %v, %v", f.outbox.envelopes[0].EventType(), f.outbox.envelopes[1].EventType())
	}
}

func TestRetryReferenceRejectsExpiredWaitWithoutWalletAccess(t *testing.T) {
	walletID, playerID := uuid.New(), uuid.New()
	pending := pendingState(walletID, playerID, retryTime)
	deadline := retryTime.Add(-time.Millisecond)
	f := newRetryDeps(walletID, playerID, ports.DuePendingReference{
		State: pending, ReferenceSchedule: ports.ReferenceSchedule{DeadlineAt: deadline, HasDeadline: true},
	})
	processed, err := f.deps().RetryDueReferences(context.Background(), 10)
	if err != nil || processed != 1 {
		t.Fatalf("processed=%d err=%v", processed, err)
	}
	updated := f.wagers.updated[0]
	if updated.Status != wagering.Rejected || updated.FailureCode != wagering.ReferenceNotFound {
		t.Fatalf("updated = %+v", updated)
	}
	if len(f.outbox.envelopes) != 1 || f.outbox.envelopes[0].EventType() != events.WagerTransactionRejected {
		t.Fatalf("envelopes = %+v", f.outbox.envelopes)
	}
	if f.wallets.locks != 0 {
		t.Fatal("expired wait must not touch the wallet")
	}
}

func TestRetryReferenceRejectsUnsuccessfulReference(t *testing.T) {
	walletID, playerID := uuid.New(), uuid.New()
	pending := pendingState(walletID, playerID, retryTime)
	f := newRetryDeps(walletID, playerID, ports.DuePendingReference{State: pending})
	// The referenced BET exists but ended REJECTED.
	rejected, err := wagering.NewExternal(wagering.Request{
		ID: uuid.New(), WalletID: walletID, PlayerID: playerID, Kind: wagering.Bet,
		Money: mustMoney("25.00"), ProviderID: "provider-a", ExternalTransactionID: "bet-1",
		IdempotencyKey: "provider-a:bet-1", PayloadHash: otherHash,
		RoundID: "round-1", GameID: "fortune-chimp",
	}, retryTime)
	if err != nil {
		t.Fatal(err)
	}
	if err := rejected.Process(walletAt(walletID, playerID, "0.00", retryTime), wagering.Reference{}, retryTime); err != nil {
		t.Fatal(err)
	}
	f.lookups.hasReference, f.lookups.reference = true, ports.ResolvedReference{State: rejected.Snapshot()}

	processed, err := f.deps().RetryDueReferences(context.Background(), 10)
	if err != nil || processed != 1 {
		t.Fatalf("processed=%d err=%v", processed, err)
	}
	if updated := f.wagers.updated[0]; updated.Status != wagering.Rejected || updated.FailureCode != wagering.ReferenceNotProcessed {
		t.Fatalf("updated = %+v", updated)
	}
	if len(f.outbox.envelopes) != 1 || f.outbox.envelopes[0].EventType() != events.WagerTransactionRejected {
		t.Fatalf("envelopes = %+v", f.outbox.envelopes)
	}
}

func TestRetryReferenceRetriesDeadlockedTransaction(t *testing.T) {
	walletID, playerID := uuid.New(), uuid.New()
	due := ports.DuePendingReference{State: pendingState(walletID, playerID, retryTime)}
	f := newRetryDeps(walletID, playerID, due)
	f.uow.failures = []error{ports.ErrRetryableTransaction}
	processed, err := f.deps().RetryDueReferences(context.Background(), 10)
	if err != nil || processed != 1 {
		t.Fatalf("processed=%d err=%v", processed, err)
	}
	if len(f.wagers.updated) != 1 {
		t.Fatalf("updates = %d", len(f.wagers.updated))
	}
}

func TestReferenceBackoffSaturatesBeforeShifting(t *testing.T) {
	deps := RetryReferenceDeps{}
	for _, tc := range []struct {
		attempt int
		want    time.Duration
	}{
		{-1, time.Second}, {0, time.Second}, {1, time.Second},
		{2, 2 * time.Second}, {9, 256 * time.Second}, {10, 5 * time.Minute},
		{33, 5 * time.Minute}, {64, 5 * time.Minute}, {1000, 5 * time.Minute},
		{int(^uint(0) >> 1), 5 * time.Minute},
	} {
		if got := deps.referenceBackoff(tc.attempt); got != tc.want {
			t.Errorf("attempt=%d: got %v, want %v", tc.attempt, got, tc.want)
		}
	}
}
