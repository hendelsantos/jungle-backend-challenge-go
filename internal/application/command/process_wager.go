package command

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/ports"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/events"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/ledger"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/money"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wagering"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wallet"
)

var (
	// ErrInvalidKind: OPENING is reserved for the internal wallet opening.
	ErrInvalidKind = errors.New("OPENING is not accepted from external providers")
	// ErrInvalidInput covers contract problems the domain never sees.
	ErrInvalidInput = errors.New("invalid wager input")
)

// maxTransactionAttempts bounds retries of deadlocks, serialization failures
// and idempotency races; each attempt re-runs the whole unit of work.
const maxTransactionAttempts = 4

type ProcessWagerInput struct {
	ProviderID                     string
	ExternalTransactionID          string
	IdempotencyKey                 string
	PlayerID                       uuid.UUID
	WalletID                       uuid.UUID
	RoundID                        string
	GameID                         string
	Kind                           wagering.Kind
	Money                          money.Money
	ReferenceExternalTransactionID string
	CorrelationID                  uuid.UUID
}

type ProcessWagerResult struct {
	TransactionID    uuid.UUID
	Status           wagering.Status
	FailureCode      wagering.FailureCode
	Balance          money.Money
	HasBalance       bool
	WalletVersion    int64
	IdempotentReplay bool
}

type ProcessWagerDeps struct {
	UnitOfWork   ports.UnitOfWork
	Wallets      ports.WalletRepository
	Wagers       ports.WagerTransactionRepository
	WagerLookups ports.WagerTransactionReader
	Ledger       ports.LedgerRepository
	Outbox       ports.OutboxRepository
	Now          func() time.Time
	NewID        func() uuid.UUID
	// Backoff spaces retry attempts; tests inject a zero duration.
	Backoff func(attempt int) time.Duration
}

// ProcessWager evaluates one external operation (BET, WIN, LOSS, REFUND,
// ROLLBACK) and commits the transaction, the wallet movement, the ledger entry
// and the outbox events atomically. Replays return the originally persisted
// result without new effects; content conflicts return ErrIdempotencyConflict.
func (d ProcessWagerDeps) ProcessWager(ctx context.Context, input ProcessWagerInput) (ProcessWagerResult, error) {
	if err := d.validate(); err != nil {
		return ProcessWagerResult{}, err
	}
	if err := validateWagerInput(input); err != nil {
		return ProcessWagerResult{}, err
	}
	hash, err := payloadHash(input)
	if err != nil {
		return ProcessWagerResult{}, fmt.Errorf("compute payload hash: %w", err)
	}
	backoff := d.Backoff
	if backoff == nil {
		backoff = defaultBackoff
	}
	var outcome ProcessWagerResult
	for attempt := 1; ; attempt++ {
		err := d.UnitOfWork.WithinTx(ctx, func(ctx context.Context) error {
			var err error
			outcome, err = d.processOnce(ctx, input, hash)
			return err
		})
		if err == nil {
			return outcome, nil
		}
		if !errors.Is(err, ports.ErrRetryableTransaction) || attempt >= maxTransactionAttempts {
			return ProcessWagerResult{}, err
		}
		if err := sleepCtx(ctx, backoff(attempt)); err != nil {
			return ProcessWagerResult{}, err
		}
	}
}

// defaultBackoff grows exponentially with jitter, capped for responsiveness.
func defaultBackoff(attempt int) time.Duration {
	base := 25 * time.Millisecond << (attempt - 1)
	if base > 200*time.Millisecond {
		base = 200 * time.Millisecond
	}
	return base + time.Duration(rand.Int63n(int64(25*time.Millisecond)))
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// findReplay reads only provider-scoped durable identities.
func (d ProcessWagerDeps) findReplay(ctx context.Context, input ProcessWagerInput, hash string) (wagering.State, bool, error) {
	// Idempotency first: a known key with the same content replays the
	// persisted result; different content or a hijacked external identity is
	// a conflict.
	existing, err := d.WagerLookups.FindByIdempotencyKey(ctx, input.ProviderID, input.IdempotencyKey)
	switch {
	case err == nil:
		if existing.Request.PayloadHash != hash {
			return wagering.State{}, false, ports.ErrIdempotencyConflict
		}
		return existing, true, nil
	case errors.Is(err, ports.ErrWagerTransactionNotFound):
	default:
		return wagering.State{}, false, err
	}
	byExternal, err := d.WagerLookups.FindByExternalID(ctx, input.ProviderID, input.ExternalTransactionID)
	switch {
	case err == nil:
		// READ COMMITTED can observe a concurrent commit between these two
		// SELECTs. The external lookup may therefore be the first to find a
		// matching replay, not necessarily an operation under another key.
		if byExternal.Request.IdempotencyKey == input.IdempotencyKey && byExternal.Request.PayloadHash == hash {
			return byExternal, true, nil
		}
		// The operation exists under another key or with different content.
		return wagering.State{}, false, ports.ErrIdempotencyConflict
	case errors.Is(err, ports.ErrWagerTransactionNotFound):
	default:
		return wagering.State{}, false, err
	}

	return wagering.State{}, false, nil
}

// processOnce runs the whole business evaluation inside one transaction.
func (d ProcessWagerDeps) processOnce(ctx context.Context, input ProcessWagerInput, hash string) (ProcessWagerResult, error) {
	if existing, found, err := d.findReplay(ctx, input, hash); err != nil {
		return ProcessWagerResult{}, err
	} else if found {
		return resultFrom(existing, true), nil
	}

	// Lock the wallet row: writers on the same wallet serialize here while
	// independent wallets proceed in parallel.
	locked, err := d.Wallets.LockWallet(ctx, input.WalletID)
	if err != nil {
		return ProcessWagerResult{}, err
	}
	// Another writer may have committed while this request waited for the
	// wallet. Recheck before evaluating rules against a newer balance.
	if existing, found, err := d.findReplay(ctx, input, hash); err != nil {
		return ProcessWagerResult{}, err
	} else if found {
		return resultFrom(existing, true), nil
	}
	if locked.Balance.Currency() != money.BRL {
		return ProcessWagerResult{}, ErrCurrencyNotAccepted
	}
	// Read the clock after acquiring the lock. Preserve monotonic wallet time
	// across processes with slight clock skew, at PostgreSQL microsecond precision.
	now := d.Now().UTC().Truncate(time.Microsecond)
	if now.IsZero() {
		return ProcessWagerResult{}, wagering.ErrInvalidTime
	}
	if now.Before(locked.UpdatedAt) {
		now = locked.UpdatedAt
	}
	w, err := wallet.Rehydrate(locked)
	if err != nil {
		return ProcessWagerResult{}, fmt.Errorf("rehydrate wallet: %w", err)
	}

	reference := wagering.Reference{}
	if input.ReferenceExternalTransactionID != "" {
		resolved, err := d.WagerLookups.FindReference(ctx, input.ProviderID, input.ReferenceExternalTransactionID)
		if err != nil && !errors.Is(err, ports.ErrWagerTransactionNotFound) {
			return ProcessWagerResult{}, err
		}
		if err == nil {
			referenced, err := wagering.Rehydrate(resolved.State)
			if err != nil {
				return ProcessWagerResult{}, fmt.Errorf("rehydrate reference: %w", err)
			}
			reference = wagering.Reference{Transaction: referenced, Compensated: resolved.Compensated}
		}
		// A missing reference stays unresolved: the domain suspends the
		// operation as PENDING_REFERENCE.
	}

	transaction, err := wagering.NewExternal(wagering.Request{
		ID:                             d.NewID(),
		WalletID:                       input.WalletID,
		PlayerID:                       input.PlayerID,
		Kind:                           input.Kind,
		Money:                          input.Money,
		ProviderID:                     input.ProviderID,
		ExternalTransactionID:          input.ExternalTransactionID,
		IdempotencyKey:                 input.IdempotencyKey,
		PayloadHash:                    hash,
		RoundID:                        input.RoundID,
		GameID:                         input.GameID,
		ReferenceExternalTransactionID: input.ReferenceExternalTransactionID,
	}, now)
	if err != nil {
		return ProcessWagerResult{}, fmt.Errorf("build wager transaction: %w", err)
	}
	// nil error means the evaluation concluded: consult Status afterwards.
	if err := transaction.Process(w, reference, now); err != nil {
		return ProcessWagerResult{}, fmt.Errorf("process wager: %w", err)
	}
	snapshot := transaction.Snapshot()
	if err := d.Wagers.InsertWagerTransaction(ctx, snapshot); err != nil {
		return ProcessWagerResult{}, err
	}

	// The ledger direction comes from the actual balance movement, not from a
	// duplicated kind policy: ROLLBACK credits or debits depending on the
	// reference kind.
	balanceChanged := w.Version() != locked.Version
	if balanceChanged {
		if err := d.Wallets.UpdateWalletBalance(ctx, w.Snapshot(), locked.Version); err != nil {
			return ProcessWagerResult{}, err
		}
		direction := ledger.Credit
		if w.Balance().MinorUnits() < locked.Balance.MinorUnits() {
			direction = ledger.Debit
		}
		entry, err := ledger.New(d.NewID(), input.WalletID, snapshot.Request.ID, direction,
			input.Money, locked.Balance, w.Balance(), now)
		if err != nil {
			return ProcessWagerResult{}, fmt.Errorf("build ledger entry: %w", err)
		}
		if err := d.Ledger.InsertLedgerEntry(ctx, entry); err != nil {
			return ProcessWagerResult{}, err
		}
	}

	envelopes, err := d.buildEvents(input, snapshot, locked, w.Snapshot(), now)
	if err != nil {
		return ProcessWagerResult{}, err
	}
	for _, envelope := range envelopes {
		if err := d.Outbox.InsertEvent(ctx, envelope); err != nil {
			return ProcessWagerResult{}, err
		}
	}
	return resultFrom(snapshot, false), nil
}

func (d ProcessWagerDeps) buildEvents(input ProcessWagerInput, snapshot wagering.State,
	before, after wallet.State, now time.Time) ([]events.Envelope, error) {
	transactionID := snapshot.Request.ID
	var envelopes []events.Envelope
	appendEvent := func(envelope events.Envelope, err error) error {
		if err != nil {
			return err
		}
		envelopes = append(envelopes, envelope)
		return nil
	}
	switch snapshot.Status {
	case wagering.Processed:
		if err := appendEvent(events.NewWagerTransactionProcessed(d.NewID(), input.CorrelationID, transactionID, now,
			events.WagerTransactionProcessedData{
				TransactionID: transactionID,
				WalletID:      input.WalletID,
				PlayerID:      input.PlayerID,
				Kind:          input.Kind,
				Money:         input.Money,
			})); err != nil {
			return nil, fmt.Errorf("build processed event: %w", err)
		}
	case wagering.Rejected:
		if err := appendEvent(events.NewWagerTransactionRejected(d.NewID(), input.CorrelationID, transactionID, now,
			events.WagerTransactionRejectedData{
				TransactionID: transactionID,
				WalletID:      input.WalletID,
				PlayerID:      input.PlayerID,
				Kind:          input.Kind,
				Money:         input.Money,
				FailureCode:   snapshot.FailureCode,
			})); err != nil {
			return nil, fmt.Errorf("build rejected event: %w", err)
		}
	case wagering.PendingReference:
		if err := appendEvent(events.NewWagerTransactionPendingReference(d.NewID(), input.CorrelationID, transactionID, now,
			events.WagerTransactionPendingReferenceData{
				TransactionID:                  transactionID,
				WalletID:                       input.WalletID,
				PlayerID:                       input.PlayerID,
				Kind:                           input.Kind,
				Money:                          input.Money,
				ReferenceExternalTransactionID: input.ReferenceExternalTransactionID,
			})); err != nil {
			return nil, fmt.Errorf("build pending reference event: %w", err)
		}
	}
	if after.Version != before.Version {
		direction := ledger.Credit
		if after.Balance.MinorUnits() < before.Balance.MinorUnits() {
			direction = ledger.Debit
		}
		if err := appendEvent(events.NewWalletBalanceChanged(d.NewID(), input.CorrelationID, transactionID, now,
			events.WalletBalanceChangedData{
				WalletID:      input.WalletID,
				TransactionID: transactionID,
				Direction:     direction,
				Money:         input.Money,
				BalanceBefore: before.Balance,
				BalanceAfter:  after.Balance,
				WalletVersion: after.Version,
			})); err != nil {
			return nil, fmt.Errorf("build balance event: %w", err)
		}
	}
	return envelopes, nil
}

func resultFrom(state wagering.State, replay bool) ProcessWagerResult {
	result := ProcessWagerResult{
		TransactionID:    state.Request.ID,
		Status:           state.Status,
		FailureCode:      state.FailureCode,
		IdempotentReplay: replay,
	}
	if state.HasResult {
		result.HasBalance = true
		result.Balance = state.Result.Balance
		result.WalletVersion = state.Result.WalletVersion
	}
	return result
}

func (d ProcessWagerDeps) validate() error {
	if d.UnitOfWork == nil || d.Wallets == nil || d.Wagers == nil ||
		d.WagerLookups == nil || d.Ledger == nil || d.Outbox == nil ||
		d.Now == nil || d.NewID == nil {
		return ErrIncompleteDeps
	}
	return nil
}

func validateWagerInput(input ProcessWagerInput) error {
	if input.Kind == wagering.Opening {
		return ErrInvalidKind
	}
	if input.PlayerID == uuid.Nil || input.WalletID == uuid.Nil || input.CorrelationID == uuid.Nil {
		return fmt.Errorf("%w: player, wallet and correlation ids are required", ErrInvalidInput)
	}
	for _, field := range [][2]string{
		{"providerId", input.ProviderID},
		{"externalTransactionId", input.ExternalTransactionID},
		{"idempotencyKey", input.IdempotencyKey},
		{"roundId", input.RoundID},
		{"gameId", input.GameID},
	} {
		if strings.TrimSpace(field[1]) == "" || !utf8.ValidString(field[1]) || strings.ContainsRune(field[1], 0) {
			return fmt.Errorf("%w: %s is required", ErrInvalidInput, field[0])
		}
	}
	if err := input.Money.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	if input.Money.Currency() != money.BRL {
		return ErrCurrencyNotAccepted
	}
	if !utf8.ValidString(input.ReferenceExternalTransactionID) || strings.ContainsRune(input.ReferenceExternalTransactionID, 0) {
		return ErrInvalidInput
	}
	return nil
}
