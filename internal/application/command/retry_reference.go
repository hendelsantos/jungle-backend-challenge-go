package command

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/ports"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/events"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/ledger"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wagering"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wallet"
)

// DefaultReferenceTTL bounds how long a missing reference may keep an
// operation suspended before it is rejected with REFERENCE_NOT_FOUND.
const DefaultReferenceTTL = 24 * time.Hour

// RetryReferenceDeps resumes PENDING_REFERENCE operations durably. The worker
// claims due rows with FOR UPDATE SKIP LOCKED, so any instance may run this
// concurrently and no pending operation is processed twice.
type RetryReferenceDeps struct {
	UnitOfWork ports.UnitOfWork
	Wallets    ports.WalletRepository
	Wagers     ports.WagerTransactionRepository
	Lookups    ports.WagerTransactionReader
	Pending    ports.PendingReferenceClaimer
	Ledger     ports.LedgerRepository
	Outbox     ports.OutboxRepository
	Now        func() time.Time
	NewID      func() uuid.UUID
	// ReferenceTTL bounds the wait; zero falls back to DefaultReferenceTTL.
	ReferenceTTL time.Duration
	// ReferenceBackoff spaces retries of an still-missing reference;
	// zero falls back to an exponential schedule capped at 5 minutes.
	ReferenceBackoff func(attempt int) time.Duration
	// OnConclusion, when set, is invoked once per pending reference that
	// reaches a terminal status inside the committed unit of work (resumed
	// PROCESSED/REJECTED or rejected by TTL). Reschedules never trigger it:
	// the metric sink (e.g. jungle_wager_operations_total) counts exactly one
	// durable conclusion per operation across channels.
	OnConclusion func(kind wagering.Kind, status wagering.Status)
}

// RetryDueReferences processes up to limit due pending references, one
// transaction each. It returns how many were resumed (resolved, rejected or
// rescheduled). A transient failure aborts the batch; the rows stay due and
// the next invocation retries them.
func (d RetryReferenceDeps) RetryDueReferences(ctx context.Context, limit int) (int, error) {
	if err := d.validate(); err != nil {
		return 0, err
	}
	if limit < 1 {
		return 0, fmt.Errorf("%w: limit must be at least 1", ErrInvalidInput)
	}
	ttl := d.ReferenceTTL
	if ttl <= 0 {
		ttl = DefaultReferenceTTL
	}
	processed := 0
	for processed < limit {
		done, err := d.retryOne(ctx, ttl)
		if err != nil {
			return processed, err
		}
		if done {
			return processed, nil
		}
		processed++
	}
	return processed, nil
}

// retryOne runs one unit of work: claim a due pending reference, resolve,
// reject or reschedule it, and commit. It reports whether no due row remained.
// Deadlocks and serialization failures retry the whole unit of work.
func (d RetryReferenceDeps) retryOne(ctx context.Context, ttl time.Duration) (bool, error) {
	for attempt := 1; ; attempt++ {
		done := false
		err := d.UnitOfWork.WithinTx(ctx, func(ctx context.Context) error {
			var err error
			done, err = d.resumeOne(ctx, ttl)
			return err
		})
		if err == nil {
			return done, nil
		}
		if !errors.Is(err, ports.ErrRetryableTransaction) || attempt >= maxTransactionAttempts {
			return false, err
		}
		if err := sleepCtx(ctx, defaultBackoff(attempt)); err != nil {
			return false, err
		}
	}
}

// resumeOne resumes a single claimed pending reference inside the caller's
// transaction. The claimed row stays locked until the commit.
func (d RetryReferenceDeps) resumeOne(ctx context.Context, ttl time.Duration) (bool, error) {
	now := d.Now().UTC().Truncate(time.Microsecond)
	if now.IsZero() {
		return false, wagering.ErrInvalidTime
	}
	due, err := d.Pending.ClaimDuePendingReferences(ctx, now, 1)
	if err != nil {
		return false, err
	}
	if len(due) == 0 {
		return true, nil
	}
	item := due[0]

	// The deadline is fixed at creation time; the worker only materializes it
	// on the first claim so the INSERT contract of ProcessWager stays intact.
	deadline := item.DeadlineAt
	if !item.HasDeadline {
		deadline = item.State.CreatedAt.Add(ttl)
	}
	// Keep wallet time monotonic across processes with slight clock skew.
	if now.Before(item.State.UpdatedAt) {
		now = item.State.UpdatedAt
	}

	transaction, err := wagering.Rehydrate(item.State)
	if err != nil {
		return false, fmt.Errorf("rehydrate pending reference: %w", err)
	}
	schedule := ports.ReferenceSchedule{Attempts: item.Attempts, DeadlineAt: deadline, HasDeadline: true}

	if now.After(deadline) {
		// The reference never arrived within the policy window: a definitive,
		// auditable rejection. No wallet was read, so no result is invented.
		if err := transaction.Reject(wagering.ReferenceNotFound, now); err != nil {
			return false, fmt.Errorf("reject expired reference: %w", err)
		}
		if err := d.Wagers.UpdateWagerTransaction(ctx, transaction.Snapshot(), schedule); err != nil {
			return false, err
		}
		if err := d.insertRejectedEvent(ctx, transaction.Snapshot(), now); err != nil {
			return false, err
		}
		// Best-effort signal after the last write of the unit of work: a
		// commit failure here rolls everything back and the next claim
		// retries — the counter approximates, never drives, the ledger.
		if d.OnConclusion != nil {
			d.OnConclusion(item.State.Request.Kind, wagering.Rejected)
		}
		return false, nil
	}

	locked, err := d.Wallets.LockWallet(ctx, item.State.Request.WalletID)
	if err != nil {
		return false, err
	}
	// Lock acquisition may wait behind another writer. Refresh the instant
	// and preserve the persisted clocks even when this process is behind.
	if afterLock := d.Now().UTC().Truncate(time.Microsecond); afterLock.After(now) {
		now = afterLock
	}
	if locked.UpdatedAt.After(now) {
		now = locked.UpdatedAt
	}
	w, err := wallet.Rehydrate(locked)
	if err != nil {
		return false, fmt.Errorf("rehydrate wallet: %w", err)
	}
	reference, err := d.resolveReference(ctx, item.State)
	if err != nil {
		return false, err
	}
	if err := transaction.Process(w, reference, now); err != nil {
		return false, fmt.Errorf("resume pending reference: %w", err)
	}
	snapshot := transaction.Snapshot()

	if snapshot.Status == wagering.PendingReference {
		// Still waiting: reschedule with a longer backoff, no events, no
		// financial effect. The original wait event is already durable.
		attempts := item.Attempts + 1
		schedule.Attempts = attempts
		schedule.NextAttemptAt = now.Add(d.referenceBackoff(attempts))
		schedule.HasNextAttempt = true
		if err := d.Wagers.UpdateWagerTransaction(ctx, snapshot, schedule); err != nil {
			return false, err
		}
		return false, nil
	}

	// Terminal outcome: persist the financial movement exactly like
	// ProcessWager — balance, ledger entry and events in this same commit.
	if w.Version() != locked.Version {
		if err := d.Wallets.UpdateWalletBalance(ctx, w.Snapshot(), locked.Version); err != nil {
			return false, err
		}
		direction := ledger.Credit
		if w.Balance().MinorUnits() < locked.Balance.MinorUnits() {
			direction = ledger.Debit
		}
		entry, err := ledger.New(d.NewID(), item.State.Request.WalletID, snapshot.Request.ID, direction,
			item.State.Request.Money, locked.Balance, w.Balance(), now)
		if err != nil {
			return false, fmt.Errorf("build ledger entry: %w", err)
		}
		if err := d.Ledger.InsertLedgerEntry(ctx, entry); err != nil {
			return false, err
		}
	}
	if err := d.insertOutcomeEvents(ctx, snapshot, locked, w.Snapshot(), now); err != nil {
		return false, err
	}
	if err := d.Wagers.UpdateWagerTransaction(ctx, snapshot, schedule); err != nil {
		return false, err
	}
	// Best-effort signal after the last write of the unit of work (same
	// approximation as the expiry path above).
	if d.OnConclusion != nil {
		d.OnConclusion(snapshot.Request.Kind, snapshot.Status)
	}
	return false, nil
}

// resolveReference loads the referenced operation scoped to the provider.
// A missing reference keeps the operation suspended.
func (d RetryReferenceDeps) resolveReference(ctx context.Context, state wagering.State) (wagering.Reference, error) {
	resolved, err := d.Lookups.FindReference(ctx, state.Request.ProviderID, state.Request.ReferenceExternalTransactionID)
	if errors.Is(err, ports.ErrWagerTransactionNotFound) {
		return wagering.Reference{}, nil
	}
	if err != nil {
		return wagering.Reference{}, err
	}
	referenced, err := wagering.Rehydrate(resolved.State)
	if err != nil {
		return wagering.Reference{}, fmt.Errorf("rehydrate reference: %w", err)
	}
	return wagering.Reference{Transaction: referenced, Compensated: resolved.Compensated}, nil
}

// insertRejectedEvent records the definitive rejection of an expired wait.
func (d RetryReferenceDeps) insertRejectedEvent(ctx context.Context, state wagering.State, now time.Time) error {
	envelope, err := events.NewWagerTransactionRejected(d.NewID(), state.Request.ID, state.Request.ID, now,
		events.WagerTransactionRejectedData{
			TransactionID: state.Request.ID,
			WalletID:      state.Request.WalletID,
			PlayerID:      state.Request.PlayerID,
			Kind:          state.Request.Kind,
			Money:         state.Request.Money,
			FailureCode:   state.FailureCode,
		})
	if err != nil {
		return fmt.Errorf("build rejected event: %w", err)
	}
	return d.Outbox.InsertEvent(ctx, envelope)
}

// insertOutcomeEvents emits the conclusion of a resumed operation. Correlation
// and causation carry the transaction identity: the original request
// correlation is not persisted on the row, and the resumed operation is both
// the cause and the tracking unit of these events.
func (d RetryReferenceDeps) insertOutcomeEvents(ctx context.Context, snapshot wagering.State,
	before, after wallet.State, now time.Time) error {
	transactionID := snapshot.Request.ID
	var envelopes []events.Envelope
	appendEnvelope := func(envelope events.Envelope, err error) error {
		if err != nil {
			return err
		}
		envelopes = append(envelopes, envelope)
		return nil
	}
	switch snapshot.Status {
	case wagering.Processed:
		if err := appendEnvelope(events.NewWagerTransactionProcessed(d.NewID(), transactionID, transactionID, now,
			events.WagerTransactionProcessedData{
				TransactionID: transactionID,
				WalletID:      snapshot.Request.WalletID,
				PlayerID:      snapshot.Request.PlayerID,
				Kind:          snapshot.Request.Kind,
				Money:         snapshot.Request.Money,
			})); err != nil {
			return fmt.Errorf("build processed event: %w", err)
		}
	case wagering.Rejected:
		if err := appendEnvelope(events.NewWagerTransactionRejected(d.NewID(), transactionID, transactionID, now,
			events.WagerTransactionRejectedData{
				TransactionID: transactionID,
				WalletID:      snapshot.Request.WalletID,
				PlayerID:      snapshot.Request.PlayerID,
				Kind:          snapshot.Request.Kind,
				Money:         snapshot.Request.Money,
				FailureCode:   snapshot.FailureCode,
			})); err != nil {
			return fmt.Errorf("build rejected event: %w", err)
		}
	default:
		return fmt.Errorf("unexpected resumed status %q", snapshot.Status)
	}
	if after.Version != before.Version {
		direction := ledger.Credit
		if after.Balance.MinorUnits() < before.Balance.MinorUnits() {
			direction = ledger.Debit
		}
		if err := appendEnvelope(events.NewWalletBalanceChanged(d.NewID(), transactionID, transactionID, now,
			events.WalletBalanceChangedData{
				WalletID:      snapshot.Request.WalletID,
				TransactionID: transactionID,
				Direction:     direction,
				Money:         snapshot.Request.Money,
				BalanceBefore: before.Balance,
				BalanceAfter:  after.Balance,
				WalletVersion: after.Version,
			})); err != nil {
			return fmt.Errorf("build balance event: %w", err)
		}
	}
	for _, envelope := range envelopes {
		if err := d.Outbox.InsertEvent(ctx, envelope); err != nil {
			return err
		}
	}
	return nil
}

func (d RetryReferenceDeps) referenceBackoff(attempt int) time.Duration {
	if d.ReferenceBackoff != nil {
		return d.ReferenceBackoff(attempt)
	}
	// Bound the exponent before shifting: Duration is int64 and a large
	// shift would wrap or become zero before a subsequent cap could help.
	if attempt <= 1 {
		return time.Second
	}
	if attempt >= 10 {
		return 5 * time.Minute
	}
	return time.Second << (attempt - 1)
}

func (d RetryReferenceDeps) validate() error {
	if d.UnitOfWork == nil || d.Wallets == nil || d.Wagers == nil ||
		d.Lookups == nil || d.Pending == nil || d.Ledger == nil || d.Outbox == nil ||
		d.Now == nil || d.NewID == nil {
		return ErrIncompleteDeps
	}
	return nil
}
