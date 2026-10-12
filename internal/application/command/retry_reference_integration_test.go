//go:build integration

// RetryDueReferences integration against real PostgreSQL: a REFUND arriving
// before its BET is rescheduled, resolved after the BET lands, rejected when
// the TTL expires, resumed exactly once under concurrent workers, and left
// due when a transient failure rolls the claim back.
package command_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hendelsantos/jungle-backend-challenge-go/internal/adapters/postgres"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/command"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/ports"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wagering"
)

// retryBackoff is longer than the whole scenario so a rescheduled pending is
// only due again when the test advances its schedule with forceDue.
const retryBackoff = 10 * time.Second

type retryFixture struct {
	retry  command.RetryReferenceDeps
	deps   command.ProcessWagerDeps
	create command.CreateWalletDeps
	pool   *pgxpool.Pool
}

func newRetryFixture(t *testing.T, ttl time.Duration) *retryFixture {
	t.Helper()
	deps, create, pool := newWagerDeps(t)
	// The retry command runs on the wall clock; the fixture creates wallets and
	// pendings on the same clock so deadlines behave deterministically.
	deps.Now = func() time.Time { return time.Now().UTC() }
	reader := postgres.NewWagerTransactionReader(pool)
	return &retryFixture{
		retry: command.RetryReferenceDeps{
			UnitOfWork:   postgres.NewUnitOfWork(pool),
			Wallets:      postgres.NewWalletRepository(),
			Wagers:       postgres.NewWagerTransactionRepository(),
			Lookups:      reader,
			Pending:      reader,
			Ledger:       postgres.NewLedgerRepository(),
			Outbox:       postgres.NewOutboxRepository(),
			Now:          func() time.Time { return time.Now().UTC() },
			NewID:        uuid.New,
			ReferenceTTL: ttl,
			ReferenceBackoff: func(int) time.Duration {
				return retryBackoff
			},
		},
		deps:   deps,
		create: create,
		pool:   pool,
	}
}

// drainDueReferences rejects every pending reference currently due with the
// minimum TTL, leaving a clean due set for the scenario. The shared test
// database is never reset, so prior runs may leave scheduled pendings behind;
// rejection is terminal and auditable, and no other test asserts on those rows.
func (f *retryFixture) drainDueReferences(t *testing.T) {
	t.Helper()
	drain := f.retry
	drain.ReferenceTTL = time.Nanosecond
	for {
		processed, err := drain.RetryDueReferences(context.Background(), 500)
		if err != nil {
			t.Fatalf("drain: %v", err)
		}
		if processed == 0 {
			return
		}
	}
}

// forceDue advances the schedule of one pending row as if its backoff had
// elapsed, without waiting in real time.
func (f *retryFixture) forceDue(t *testing.T, id uuid.UUID) {
	t.Helper()
	_, err := f.pool.Exec(context.Background(),
		`UPDATE wager_transactions SET next_attempt_at = now() - interval '1 second' WHERE id = $1`, id)
	if err != nil {
		t.Fatal(err)
	}
}

// refundBeforeBet persists a REFUND whose BET reference has not arrived yet.
func (f *retryFixture) refundBeforeBet(t *testing.T, walletID, playerID uuid.UUID, suffix string) wagering.State {
	t.Helper()
	input := operation(wagering.Refund, walletID, playerID, "25.00", suffix)
	bet := operation(wagering.Bet, walletID, playerID, "25.00", "ref-"+suffix)
	input.ReferenceExternalTransactionID = bet.ExternalTransactionID
	result, err := f.deps.ProcessWager(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != wagering.PendingReference {
		t.Fatalf("status = %s, want PENDING_REFERENCE", result.Status)
	}
	state, err := postgres.NewWagerTransactionReader(f.pool).FindByExternalID(context.Background(),
		"provider-a", input.ExternalTransactionID)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestRetryReferenceReschedulesThenResolvesIntegration(t *testing.T) {
	f := newRetryFixture(t, 24*time.Hour)
	f.drainDueReferences(t)
	walletID, playerID, _ := openWallet(t, f.create, "100.00")
	pending := f.refundBeforeBet(t, walletID, playerID, "resolve-refund")

	// First pass: the reference has not arrived; the worker reschedules.
	if processed, err := f.retry.RetryDueReferences(context.Background(), 10); err != nil || processed != 1 {
		t.Fatalf("processed=%d err=%v", processed, err)
	}
	var attempts int
	var nextAttempt, deadlineAt *time.Time
	if err := f.pool.QueryRow(context.Background(),
		`SELECT attempt_count, next_attempt_at, reference_deadline_at
		 FROM wager_transactions WHERE id = $1`, pending.Request.ID,
	).Scan(&attempts, &nextAttempt, &deadlineAt); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || nextAttempt == nil || deadlineAt == nil {
		t.Fatalf("schedule after first pass: attempts=%d next=%v deadline=%v", attempts, nextAttempt, deadlineAt)
	}

	// The BET lands and is processed; the schedule elapses; the worker resolves.
	bet := operation(wagering.Bet, walletID, playerID, "25.00", "ref-resolve-refund")
	if _, err := f.deps.ProcessWager(context.Background(), bet); err != nil {
		t.Fatal(err)
	}
	f.forceDue(t, pending.Request.ID)
	if processed, err := f.retry.RetryDueReferences(context.Background(), 10); err != nil || processed != 1 {
		t.Fatalf("processed=%d err=%v", processed, err)
	}
	// A third pass finds nothing due.
	if processed, err := f.retry.RetryDueReferences(context.Background(), 10); err != nil || processed != 0 {
		t.Fatalf("processed=%d err=%v", processed, err)
	}

	state, err := postgres.NewWagerTransactionReader(f.pool).FindByID(context.Background(), "provider-a", pending.Request.ID)
	if err != nil {
		t.Fatalf("lookup resumed refund: %v", err)
	}
	if state.Status != wagering.Processed || !state.HasResult || state.Result.Balance.MinorUnits() != 10000 {
		t.Fatalf("state = %+v", state)
	}
	if state.ReferenceID == uuid.Nil {
		t.Fatal("resolved reference id missing")
	}
	// Wallet ends at 100 - 25 (bet) + 25 (refund) = 100.00, version 3.
	if balance, version := walletBalance(t, f.pool, walletID); balance != 10000 || version != 3 {
		t.Fatalf("balance=%d version=%d", balance, version)
	}
	if entries := countRows(t, f.pool,
		"SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1", walletID); entries != 3 {
		t.Fatalf("ledger entries = %d", entries)
	}
}

func TestRetryReferenceRejectsExpiredTTLIntegration(t *testing.T) {
	f := newRetryFixture(t, 30*time.Millisecond)
	f.drainDueReferences(t)
	walletID, playerID, _ := openWallet(t, f.create, "100.00")
	pending := f.refundBeforeBet(t, walletID, playerID, "expire-refund")

	if processed, err := f.retry.RetryDueReferences(context.Background(), 10); err != nil || processed != 1 {
		t.Fatalf("processed=%d err=%v", processed, err)
	}
	time.Sleep(40 * time.Millisecond)
	f.forceDue(t, pending.Request.ID)
	if processed, err := f.retry.RetryDueReferences(context.Background(), 10); err != nil || processed != 1 {
		t.Fatalf("processed=%d err=%v", processed, err)
	}

	state, err := postgres.NewWagerTransactionReader(f.pool).FindByID(context.Background(), "provider-a", pending.Request.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != wagering.Rejected || state.FailureCode != wagering.ReferenceNotFound {
		t.Fatalf("state = %+v", state)
	}
	if entries := countRows(t, f.pool,
		"SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1", walletID); entries != 1 {
		t.Fatalf("ledger entries = %d, want opening only", entries)
	}
	if balance, version := walletBalance(t, f.pool, walletID); balance != 10000 || version != 1 {
		t.Fatalf("expired wait changed the wallet: balance=%d version=%d", balance, version)
	}
	if processed, err := f.retry.RetryDueReferences(context.Background(), 10); err != nil || processed != 0 {
		t.Fatalf("processed=%d err=%v after terminal rejection", processed, err)
	}
	if events := countRows(t, f.pool,
		`SELECT count(*) FROM outbox_events WHERE event_type = 'WagerTransactionRejected' AND payload_json->'data'->>'transactionId' = $1`,
		pending.Request.ID.String()); events != 1 {
		t.Fatalf("rejection events = %d", events)
	}
}

func TestRetryReferenceConcurrentWorkersProcessEachPendingOnceIntegration(t *testing.T) {
	f := newRetryFixture(t, 24*time.Hour)
	f.drainDueReferences(t)
	const pendingCount = 8
	wallets := make([]uuid.UUID, pendingCount)
	pendingIDs := make([]uuid.UUID, pendingCount)
	var wg sync.WaitGroup
	for i := range pendingCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			walletID, playerID, _ := openWallet(t, f.create, "100.00")
			pending := f.refundBeforeBet(t, walletID, playerID, "concurrent-"+uuid.NewString()[:8])
			wallets[i], pendingIDs[i] = walletID, pending.Request.ID
		}()
	}
	wg.Wait()

	// Two independent workers race over the same due rows; the 10s backoff
	// keeps rescheduled rows out of the race, so each pending is claimed once.
	const workers = 2
	results := make([]int, workers)
	errs := make([]error, workers)
	start := make(chan struct{})
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results[i], errs[i] = f.retry.RetryDueReferences(context.Background(), pendingCount)
		}()
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: %v", i, err)
		}
	}
	if total := results[0] + results[1]; total != pendingCount {
		t.Fatalf("workers resumed %d pending operations, want %d", total, pendingCount)
	}
	// No wallet moved and every row was claimed exactly once.
	for _, walletID := range wallets {
		if balance, version := walletBalance(t, f.pool, walletID); balance != 10000 || version != 1 {
			t.Fatalf("wallet %s moved: balance=%d version=%d", walletID, balance, version)
		}
	}
	var attempts int64
	if err := f.pool.QueryRow(context.Background(),
		`SELECT coalesce(sum(attempt_count),0) FROM wager_transactions WHERE id = ANY($1)`, pendingIDs,
	).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != pendingCount {
		t.Fatalf("sum of attempt_count = %d, want %d", attempts, pendingCount)
	}
}

func TestRetryReferenceTransientFailureKeepsRowDueIntegration(t *testing.T) {
	f := newRetryFixture(t, 24*time.Hour)
	f.drainDueReferences(t)
	walletID, playerID, _ := openWallet(t, f.create, "100.00")
	pending := f.refundBeforeBet(t, walletID, playerID, "transient-refund")

	// A transient failure in the schedule write rolls the claim back; the row
	// stays due instead of being lost or half processed.
	realWagers := f.retry.Wagers
	f.retry.Wagers = transientWagerUpdates{}
	if _, err := f.retry.RetryDueReferences(context.Background(), 10); err == nil {
		t.Fatal("expected transient failure")
	}

	// Restore the real repository: the same row is claimed again and rescheduled.
	f.retry.Wagers = realWagers
	if processed, err := f.retry.RetryDueReferences(context.Background(), 10); err != nil || processed != 1 {
		t.Fatalf("processed=%d err=%v", processed, err)
	}
	state, err := postgres.NewWagerTransactionReader(f.pool).FindByID(context.Background(), "provider-a", pending.Request.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != wagering.PendingReference {
		t.Fatalf("status = %s", state.Status)
	}
	if entries := countRows(t, f.pool,
		"SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1", walletID); entries != 1 {
		t.Fatalf("ledger entries = %d, want opening only", entries)
	}
}

type transientWagerUpdates struct{}

func (transientWagerUpdates) InsertWagerTransaction(context.Context, wagering.State) error {
	return errors.New("unused by the retry command")
}

func (transientWagerUpdates) UpdateWagerTransaction(context.Context, wagering.State, ports.ReferenceSchedule) error {
	return errors.New("transient schedule write failure")
}
