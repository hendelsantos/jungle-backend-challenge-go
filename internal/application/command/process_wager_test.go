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

var processTime = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

// otherHash is any sha-256 hex distinct from the hash of the bet fixture.
const otherHash = "0000000000000000000000000000000000000000000000000000000000000000"

// --- fakes -----------------------------------------------------------------

type recordingUoW struct {
	failures []error
	calls    int
}

func (f *recordingUoW) WithinTx(ctx context.Context, fn func(context.Context) error) error {
	f.calls++
	if f.calls <= len(f.failures) {
		return f.failures[f.calls-1]
	}
	return fn(ctx)
}

type processWallets struct {
	locked    wallet.State
	lockErr   error
	updates   []wallet.State
	updateErr error
}

func (f *processWallets) InsertWallet(context.Context, wallet.State) error {
	return errors.New("not used by process wager")
}

func (f *processWallets) LockWallet(context.Context, uuid.UUID) (wallet.State, error) {
	return f.locked, f.lockErr
}

func (f *processWallets) UpdateWalletBalance(_ context.Context, state wallet.State, _ int64) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	f.updates = append(f.updates, state)
	return nil
}

type processWagers struct {
	states []wagering.State
	err    error
}

func (f *processWagers) InsertWagerTransaction(_ context.Context, state wagering.State) error {
	if f.err != nil {
		return f.err
	}
	f.states = append(f.states, state)
	return nil
}

func (f *processWagers) UpdateWagerTransaction(_ context.Context, state wagering.State, _ ports.ReferenceSchedule) error {
	return f.err
}

type processLookups struct {
	byKey        wagering.State
	hasKey       bool
	byExternal   wagering.State
	hasExternal  bool
	reference    ports.ResolvedReference
	hasReference bool
}

func (f *processLookups) FindByIdempotencyKey(context.Context, string, string) (wagering.State, error) {
	if f.hasKey {
		return f.byKey, nil
	}
	return wagering.State{}, ports.ErrWagerTransactionNotFound
}

func (f *processLookups) FindByExternalID(context.Context, string, string) (wagering.State, error) {
	if f.hasExternal {
		return f.byExternal, nil
	}
	return wagering.State{}, ports.ErrWagerTransactionNotFound
}

func (f *processLookups) FindReference(context.Context, string, string) (ports.ResolvedReference, error) {
	if f.hasReference {
		return f.reference, nil
	}
	return ports.ResolvedReference{}, ports.ErrWagerTransactionNotFound
}

type processLedger struct {
	entries []ledger.Entry
	err     error
}

func (f *processLedger) InsertLedgerEntry(_ context.Context, entry ledger.Entry) error {
	if f.err != nil {
		return f.err
	}
	f.entries = append(f.entries, entry)
	return nil
}

type processOutbox struct {
	envelopes []events.Envelope
}

func (f *processOutbox) InsertEvent(_ context.Context, envelope events.Envelope) error {
	f.envelopes = append(f.envelopes, envelope)
	return nil
}

type processFakes struct {
	uow     *recordingUoW
	wallets *processWallets
	wagers  *processWagers
	lookups *processLookups
	ledger  *processLedger
	outbox  *processOutbox
}

func newProcessFakes(locked wallet.State) *processFakes {
	return &processFakes{
		uow:     &recordingUoW{},
		wallets: &processWallets{locked: locked},
		wagers:  &processWagers{},
		lookups: &processLookups{},
		ledger:  &processLedger{},
		outbox:  &processOutbox{},
	}
}

func (f *processFakes) deps() ProcessWagerDeps {
	return ProcessWagerDeps{
		UnitOfWork:   f.uow,
		Wallets:      f.wallets,
		Wagers:       f.wagers,
		WagerLookups: f.lookups,
		Ledger:       f.ledger,
		Outbox:       f.outbox,
		Now:          func() time.Time { return processTime },
		NewID:        uuid.New,
		Backoff:      func(int) time.Duration { return 0 },
	}
}

func lockedWalletState(balance int64) wallet.State {
	return wallet.State{
		ID: uuid.New(), PlayerID: uuid.New(),
		Balance: brl(balance), Version: 1,
		CreatedAt: processTime, UpdatedAt: processTime,
	}
}

func betInput(locked wallet.State, cents int64) ProcessWagerInput {
	return ProcessWagerInput{
		ProviderID:            "provider-a",
		ExternalTransactionID: "transaction-bet",
		IdempotencyKey:        "provider-a:transaction-bet",
		PlayerID:              locked.PlayerID,
		WalletID:              locked.ID,
		RoundID:               "round-1",
		GameID:                "fortune-chimp",
		Kind:                  wagering.Bet,
		Money:                 brl(cents),
		CorrelationID:         uuid.New(),
	}
}

// --- tests -----------------------------------------------------------------

func TestProcessWagerBetProcessed(t *testing.T) {
	locked := lockedWalletState(10000)
	f := newProcessFakes(locked)
	deps := f.deps()
	input := betInput(locked, 2500)

	result, err := deps.ProcessWager(context.Background(), input)
	if err != nil {
		t.Fatalf("ProcessWager(): %v", err)
	}
	if result.Status != wagering.Processed || result.IdempotentReplay ||
		!result.HasBalance || result.Balance.MinorUnits() != 7500 || result.WalletVersion != 2 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(f.wallets.updates) != 1 || f.wallets.updates[0].Balance.MinorUnits() != 7500 {
		t.Fatalf("wallet updates: %+v", f.wallets.updates)
	}
	if len(f.ledger.entries) != 1 {
		t.Fatalf("ledger entries: %d", len(f.ledger.entries))
	}
	entry := f.ledger.entries[0]
	if entry.Direction() != ledger.Debit || entry.Amount().MinorUnits() != 2500 ||
		entry.BalanceBefore().MinorUnits() != 10000 || entry.BalanceAfter().MinorUnits() != 7500 {
		t.Fatalf("unexpected ledger entry: %+v", entry)
	}
	if len(f.wagers.states) != 1 || f.wagers.states[0].Status != wagering.Processed {
		t.Fatalf("wager states: %+v", f.wagers.states)
	}
	if len(f.outbox.envelopes) != 2 {
		t.Fatalf("outbox events: %d", len(f.outbox.envelopes))
	}
	if f.outbox.envelopes[0].EventType() != events.WagerTransactionProcessed {
		t.Fatalf("first event: %v", f.outbox.envelopes[0].EventType())
	}
	if f.outbox.envelopes[1].EventType() != events.WalletBalanceChanged {
		t.Fatalf("second event: %v", f.outbox.envelopes[1].EventType())
	}
}

func TestProcessWagerReplayReturnsPersistedResult(t *testing.T) {
	locked := lockedWalletState(10000)
	f := newProcessFakes(locked)
	input := betInput(locked, 2500)
	hash, err := payloadHash(input)
	if err != nil {
		t.Fatal(err)
	}
	f.lookups.hasKey = true
	f.lookups.byKey = wagering.State{
		Request:   wagering.Request{ID: uuid.New(), PayloadHash: hash, Money: brl(2500)},
		Status:    wagering.Processed,
		Result:    wagering.Result{Balance: brl(7500), WalletVersion: 2},
		HasResult: true,
		CreatedAt: processTime, UpdatedAt: processTime,
	}

	result, err := f.deps().ProcessWager(context.Background(), input)
	if err != nil {
		t.Fatalf("ProcessWager(): %v", err)
	}
	if !result.IdempotentReplay || result.Status != wagering.Processed ||
		result.Balance.MinorUnits() != 7500 || result.WalletVersion != 2 {
		t.Fatalf("unexpected replay result: %+v", result)
	}
	if len(f.wallets.updates) != 0 || len(f.ledger.entries) != 0 ||
		len(f.wagers.states) != 0 || len(f.outbox.envelopes) != 0 {
		t.Fatal("replay must not produce any new effect")
	}
}

func TestProcessWagerIdempotencyConflicts(t *testing.T) {
	locked := lockedWalletState(10000)
	input := betInput(locked, 2500)

	t.Run("same key with different content", func(t *testing.T) {
		f := newProcessFakes(locked)
		f.lookups.hasKey = true
		f.lookups.byKey = wagering.State{
			Request: wagering.Request{ID: uuid.New(), PayloadHash: otherHash, Money: brl(2500)},
			Status:  wagering.Processed, Result: wagering.Result{Balance: brl(1), WalletVersion: 1},
			HasResult: true, CreatedAt: processTime, UpdatedAt: processTime,
		}
		if _, err := f.deps().ProcessWager(context.Background(), input); !errors.Is(err, ports.ErrIdempotencyConflict) {
			t.Fatalf("got %v, want ErrIdempotencyConflict", err)
		}
	})

	t.Run("same external id under another key", func(t *testing.T) {
		f := newProcessFakes(locked)
		f.lookups.hasExternal = true
		if _, err := f.deps().ProcessWager(context.Background(), input); !errors.Is(err, ports.ErrIdempotencyConflict) {
			t.Fatalf("got %v, want ErrIdempotencyConflict", err)
		}
		if len(f.wallets.updates) != 0 || len(f.ledger.entries) != 0 {
			t.Fatal("conflict must stop before any financial effect")
		}
	})
}

func TestProcessWagerInsufficientFundsIsABusinessRejection(t *testing.T) {
	locked := lockedWalletState(10000)
	f := newProcessFakes(locked)
	deps := f.deps()

	result, err := deps.ProcessWager(context.Background(), betInput(locked, 20000))
	if err != nil {
		t.Fatalf("ProcessWager(): %v", err)
	}
	if result.Status != wagering.Rejected || result.FailureCode != wagering.InsufficientFunds {
		t.Fatalf("unexpected result: %+v", result)
	}
	if !result.HasBalance || result.Balance.MinorUnits() != 10000 {
		t.Fatalf("rejection must report the observed balance: %+v", result)
	}
	if len(f.wallets.updates) != 0 || len(f.ledger.entries) != 0 {
		t.Fatal("rejection must not move the wallet")
	}
	if len(f.outbox.envelopes) != 1 || f.outbox.envelopes[0].EventType() != events.WagerTransactionRejected {
		t.Fatalf("outbox events: %+v", f.outbox.envelopes)
	}
}

func TestProcessWagerLossProducesNoLedgerOrBalanceChange(t *testing.T) {
	locked := lockedWalletState(10000)
	f := newProcessFakes(locked)
	deps := f.deps()
	input := betInput(locked, 0)
	input.Kind = wagering.Loss

	result, err := deps.ProcessWager(context.Background(), input)
	if err != nil {
		t.Fatalf("ProcessWager(): %v", err)
	}
	if result.Status != wagering.Processed || !result.HasBalance || result.WalletVersion != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(f.wallets.updates) != 0 || len(f.ledger.entries) != 0 {
		t.Fatal("LOSS must not touch wallet or ledger")
	}
	if len(f.outbox.envelopes) != 1 || f.outbox.envelopes[0].EventType() != events.WagerTransactionProcessed {
		t.Fatalf("LOSS must emit exactly one processed event: %+v", f.outbox.envelopes)
	}
}

func TestProcessWagerPendingReferenceWhenReferenceMissing(t *testing.T) {
	locked := lockedWalletState(10000)
	f := newProcessFakes(locked)
	deps := f.deps()
	input := betInput(locked, 2500)
	input.Kind = wagering.Refund
	input.ReferenceExternalTransactionID = "transaction-missing-bet"

	result, err := deps.ProcessWager(context.Background(), input)
	if err != nil {
		t.Fatalf("ProcessWager(): %v", err)
	}
	if result.Status != wagering.PendingReference || result.HasBalance {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(f.wagers.states) != 1 || f.wagers.states[0].Status != wagering.PendingReference {
		t.Fatalf("pending must be durable: %+v", f.wagers.states)
	}
	if len(f.wallets.updates) != 0 || len(f.ledger.entries) != 0 {
		t.Fatal("pending reference must not move the wallet")
	}
	if len(f.outbox.envelopes) != 1 || f.outbox.envelopes[0].EventType() != events.WagerTransactionPendingReference {
		t.Fatalf("outbox events: %+v", f.outbox.envelopes)
	}
}

func TestProcessWagerWalletNotFoundPropagates(t *testing.T) {
	f := newProcessFakes(lockedWalletState(10000))
	f.wallets.lockErr = ports.ErrWalletNotFound
	if _, err := f.deps().ProcessWager(context.Background(), betInput(lockedWalletState(10000), 2500)); !errors.Is(err, ports.ErrWalletNotFound) {
		t.Fatalf("got %v, want ErrWalletNotFound", err)
	}
}

func TestProcessWagerRetriesRetryableFailures(t *testing.T) {
	locked := lockedWalletState(10000)
	f := newProcessFakes(locked)
	f.uow.failures = []error{
		errors.Join(ports.ErrRetryableTransaction, errors.New("deadlock detected")),
		errors.Join(ports.ErrRetryableTransaction, errors.New("deadlock detected")),
	}

	result, err := f.deps().ProcessWager(context.Background(), betInput(locked, 2500))
	if err != nil {
		t.Fatalf("ProcessWager(): %v", err)
	}
	if f.uow.calls != 3 {
		t.Fatalf("attempts: %d, want 3", f.uow.calls)
	}
	if result.Status != wagering.Processed {
		t.Fatalf("unexpected result after retries: %+v", result)
	}
}

func TestProcessWagerDoesNotRetryOtherFailures(t *testing.T) {
	locked := lockedWalletState(10000)
	f := newProcessFakes(locked)
	f.uow.failures = []error{ports.ErrIdempotencyConflict}

	_, err := f.deps().ProcessWager(context.Background(), betInput(locked, 2500))
	if !errors.Is(err, ports.ErrIdempotencyConflict) {
		t.Fatalf("got %v", err)
	}
	if f.uow.calls != 1 {
		t.Fatalf("non-retryable failure must not be retried: %d calls", f.uow.calls)
	}
}

func TestProcessWagerGivesUpAfterBoundedAttempts(t *testing.T) {
	locked := lockedWalletState(10000)
	f := newProcessFakes(locked)
	f.uow.failures = []error{
		errors.Join(ports.ErrRetryableTransaction, errors.New("deadlock")),
		errors.Join(ports.ErrRetryableTransaction, errors.New("deadlock")),
		errors.Join(ports.ErrRetryableTransaction, errors.New("deadlock")),
		errors.Join(ports.ErrRetryableTransaction, errors.New("deadlock")),
		errors.Join(ports.ErrRetryableTransaction, errors.New("deadlock")),
	}

	_, err := f.deps().ProcessWager(context.Background(), betInput(locked, 2500))
	if !errors.Is(err, ports.ErrRetryableTransaction) {
		t.Fatalf("got %v", err)
	}
	if f.uow.calls != maxTransactionAttempts {
		t.Fatalf("attempts: %d, want %d", f.uow.calls, maxTransactionAttempts)
	}
}

func TestProcessWagerRejectsInvalidInput(t *testing.T) {
	locked := lockedWalletState(10000)
	cases := []struct {
		name    string
		mutate  func(in *ProcessWagerInput)
		wantErr error
	}{
		{"opening kind", func(in *ProcessWagerInput) { in.Kind = wagering.Opening }, ErrInvalidKind},
		{"unknown kind", func(in *ProcessWagerInput) { in.Kind = wagering.Kind("GIFT") }, wagering.ErrInvalidKind},
		{"blank provider", func(in *ProcessWagerInput) { in.ProviderID = "  " }, ErrInvalidInput},
		{"blank external id", func(in *ProcessWagerInput) { in.ExternalTransactionID = "" }, ErrInvalidInput},
		{"blank idempotency key", func(in *ProcessWagerInput) { in.IdempotencyKey = "" }, ErrInvalidInput},
		{"nil wallet id", func(in *ProcessWagerInput) { in.WalletID = uuid.Nil }, ErrInvalidInput},
		{"nil correlation id", func(in *ProcessWagerInput) { in.CorrelationID = uuid.Nil }, ErrInvalidInput},
		{"uninitialized money", func(in *ProcessWagerInput) { in.Money = money.Money{} }, ErrInvalidInput},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newProcessFakes(locked)
			input := betInput(locked, 2500)
			tc.mutate(&input)
			if _, err := f.deps().ProcessWager(context.Background(), input); !errors.Is(err, tc.wantErr) {
				t.Fatalf("got %v, want %v", err, tc.wantErr)
			}
			if len(f.wagers.states) != 0 || len(f.ledger.entries) != 0 || len(f.outbox.envelopes) != 0 {
				t.Fatal("invalid input must not reach any port")
			}
		})
	}
}

func TestProcessWagerRejectsIncompleteDeps(t *testing.T) {
	f := newProcessFakes(lockedWalletState(10000))
	deps := f.deps()
	deps.WagerLookups = nil
	if _, err := deps.ProcessWager(context.Background(), betInput(lockedWalletState(10000), 2500)); !errors.Is(err, ErrIncompleteDeps) {
		t.Fatalf("got %v, want ErrIncompleteDeps", err)
	}
}

func TestPayloadHashIsCanonicalAndStable(t *testing.T) {
	input := betInput(wallet.State{ID: uuid.MustParse("11111111-1111-1111-1111-111111111111"), PlayerID: uuid.MustParse("22222222-2222-2222-2222-222222222222")}, 2500)
	input.ReferenceExternalTransactionID = "transaction-ref"

	first, err := payloadHash(input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := payloadHash(input)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first != "daf5ba7ebaee6541a9b6d424b89e64925d9f077eb14aa6773b1d79c62d1016f3" {
		t.Fatalf("hash must be a stable sha-256 hex: %q", first)
	}

	// Amount normalization: "25.0" and "25.00" are the same operation.
	normalized := input
	normalized.Money = brl(2500)
	oneDecimal, err := money.ParseDecimal("25.0", money.BRL)
	if err != nil {
		t.Fatal(err)
	}
	normalized.Money = oneDecimal
	again, err := payloadHash(normalized)
	if err != nil {
		t.Fatal(err)
	}
	if again != first {
		t.Fatalf("normalized amounts must hash identically: %q vs %q", again, first)
	}

	// The idempotency key never participates in the hash.
	rekeyed := input
	rekeyed.IdempotencyKey = "provider-a:another-key"
	rekeyed.CorrelationID = uuid.New()
	rekeyedHash, err := payloadHash(rekeyed)
	if err != nil {
		t.Fatal(err)
	}
	if rekeyedHash != first {
		t.Fatal("idempotency key must not influence the payload hash")
	}

	// Any business field change must change the hash.
	mutations := map[string]func(in *ProcessWagerInput){
		"amount":    func(in *ProcessWagerInput) { in.Money = brl(2501) },
		"kind":      func(in *ProcessWagerInput) { in.Kind = wagering.Win },
		"round":     func(in *ProcessWagerInput) { in.RoundID = "round-2" },
		"reference": func(in *ProcessWagerInput) { in.ReferenceExternalTransactionID = "" },
		"player":    func(in *ProcessWagerInput) { in.PlayerID = uuid.New() },
		"provider":  func(in *ProcessWagerInput) { in.ProviderID = "provider-b" },
		"external":  func(in *ProcessWagerInput) { in.ExternalTransactionID = "transaction-other" },
	}
	for name, mutate := range mutations {
		mutated := input
		mutate(&mutated)
		mutatedHash, err := payloadHash(mutated)
		if err != nil {
			t.Fatal(err)
		}
		if mutatedHash == first {
			t.Fatalf("changing %s must change the hash", name)
		}
	}
}

func TestProcessWagerClockSkewPreservesWalletTime(t *testing.T) {
	locked := lockedWalletState(10000)
	locked.UpdatedAt = processTime.Add(time.Second)
	f := newProcessFakes(locked)
	result, err := f.deps().ProcessWager(t.Context(), betInput(locked, 2500))
	if err != nil || result.Status != wagering.Processed {
		t.Fatalf("clock skew: %+v %v", result, err)
	}
	if !f.wallets.updates[0].UpdatedAt.Equal(locked.UpdatedAt) {
		t.Fatal("wallet timestamp moved backwards")
	}
}

func TestProcessWagerBRLOnlyAndValidText(t *testing.T) {
	for _, name := range []string{"input currency", "wallet currency", "invalid utf8", "nul"} {
		t.Run(name, func(t *testing.T) {
			locked := lockedWalletState(10000)
			f := newProcessFakes(locked)
			input := betInput(locked, 2500)
			want := ErrCurrencyNotAccepted
			switch name {
			case "input currency":
				input.Money, _ = money.FromMinorUnits(2500, money.USD)
			case "wallet currency":
				f.wallets.locked.Balance, _ = money.FromMinorUnits(10000, money.USD)
			case "invalid utf8":
				input.GameID = string([]byte{0xff})
				want = ErrInvalidInput
			case "nul":
				input.ReferenceExternalTransactionID = "bad\x00reference"
				want = ErrInvalidInput
			}
			if _, err := f.deps().ProcessWager(t.Context(), input); !errors.Is(err, want) {
				t.Fatalf("got %v want %v", err, want)
			}
			if len(f.wagers.states)+len(f.ledger.entries)+len(f.outbox.envelopes) != 0 {
				t.Fatal("invalid request persisted effects")
			}
		})
	}
}

func TestProcessWagerCancelledBackoff(t *testing.T) {
	locked := lockedWalletState(10000)
	f := newProcessFakes(locked)
	f.uow.failures = []error{ports.ErrRetryableTransaction}
	deps := f.deps()
	deps.Backoff = func(int) time.Duration { return time.Hour }
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := deps.ProcessWager(ctx, betInput(locked, 2500)); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if f.uow.calls != 1 {
		t.Fatalf("attempts=%d", f.uow.calls)
	}
}

func TestProcessWagerReplayCommittedBetweenIdentityLookups(t *testing.T) {
	locked := lockedWalletState(10000)
	f := newProcessFakes(locked)
	input := betInput(locked, 2500)
	hash, err := payloadHash(input)
	if err != nil {
		t.Fatal(err)
	}
	// The key SELECT sees no row. A concurrent commit becomes visible to the
	// next SELECT by external identity under READ COMMITTED.
	f.lookups.hasExternal = true
	f.lookups.byExternal = wagering.State{
		Request: wagering.Request{ID: uuid.New(), IdempotencyKey: input.IdempotencyKey, PayloadHash: hash},
		Status:  wagering.Processed, HasResult: true, Result: wagering.Result{Balance: brl(7500), WalletVersion: 2},
	}
	result, err := f.deps().ProcessWager(t.Context(), input)
	if err != nil || !result.IdempotentReplay || result.TransactionID != f.lookups.byExternal.Request.ID || result.Balance.MinorUnits() != 7500 {
		t.Fatalf("replay: %+v %v", result, err)
	}
	if len(f.wagers.states)+len(f.ledger.entries)+len(f.outbox.envelopes) != 0 {
		t.Fatal("replay created effects")
	}
}
