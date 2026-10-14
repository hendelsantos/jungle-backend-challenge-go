package command

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/ports"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wagering"
)

// ErrPermanentMessage marks a consumed message that can never succeed: a
// broken contract or a redelivery that rewrote its content. The consumer
// dead-letters it instead of retrying; the domain never saw an effect.
var ErrPermanentMessage = errors.New("message is permanently invalid")

// ConsumeWagerDeps processes one SQS message: the wager evaluation and the
// inbox conclusion commit atomically, so a crash before the commit leaves the
// message unconsumed and the redelivery reprocesses safely.
type ConsumeWagerDeps struct {
	UnitOfWork ports.UnitOfWork
	Process    ProcessWagerDeps
	Inbox      ports.InboxRepository
	Now        func() time.Time
	// Backoff spaces transaction retries; tests inject a zero duration.
	Backoff func(attempt int) time.Duration
	OnRetry func()
}

type ConsumeWagerInput struct {
	ConsumerName string
	MessageID    string
	PayloadHash  string
	Wager        ProcessWagerInput
}

// ConsumeWagerResult reports the durable outcome of one message.
type ConsumeWagerResult struct {
	TransactionID uuid.UUID
	Status        wagering.Status
	FailureCode   wagering.FailureCode
	// Replay is the business-level idempotent replay of ProcessWager.
	Replay bool
	// InboxReplay means this exact message identity was already completed in
	// a previous commit: nothing was written again.
	InboxReplay bool
}

// ConsumeWagerMessage evaluates the wager and completes the inbox in one SQL
// transaction. Transaction retries re-run everything: the previous attempt
// rolled back completely. Permanent classification stops retries and tells
// the consumer to dead-letter the message.
func (d ConsumeWagerDeps) ConsumeWagerMessage(ctx context.Context, input ConsumeWagerInput) (ConsumeWagerResult, error) {
	if err := d.validate(); err != nil {
		return ConsumeWagerResult{}, err
	}
	if strings.TrimSpace(input.ConsumerName) == "" || strings.TrimSpace(input.MessageID) == "" ||
		!utf8.ValidString(input.ConsumerName) || strings.ContainsRune(input.ConsumerName, 0) || len(input.ConsumerName) > 256 ||
		!utf8.ValidString(input.MessageID) || strings.ContainsRune(input.MessageID, 0) || len(input.MessageID) > 256 {
		return ConsumeWagerResult{}, fmt.Errorf("%w: invalid consumer/message identity", ErrPermanentMessage)
	}
	hash, err := hex.DecodeString(input.PayloadHash)
	if err != nil || len(hash) != 32 {
		return ConsumeWagerResult{}, fmt.Errorf("%w: invalid payload hash", ErrPermanentMessage)
	}
	backoff := d.Backoff
	if backoff == nil {
		backoff = defaultBackoff
	}
	var result ConsumeWagerResult
	for attempt := 1; ; attempt++ {
		err := d.UnitOfWork.WithinTx(ctx, func(ctx context.Context) error {
			var err error
			result, err = d.consumeOnce(ctx, input)
			return err
		})
		if err == nil {
			return result, nil
		}
		if errors.Is(err, ErrPermanentMessage) {
			return ConsumeWagerResult{}, err
		}
		if !errors.Is(err, ports.ErrRetryableTransaction) || attempt >= maxTransactionAttempts {
			return ConsumeWagerResult{}, err
		}
		if d.OnRetry != nil {
			d.OnRetry()
		}
		if err := sleepCtx(ctx, backoff(attempt)); err != nil {
			return ConsumeWagerResult{}, err
		}
	}
}

// consumeOnce runs inside the caller's transaction. The nested ProcessWager
// joins this transaction: one commit covers wallet, transaction, ledger,
// outbox events and the inbox conclusion.
func (d ConsumeWagerDeps) consumeOnce(ctx context.Context, input ConsumeWagerInput) (ConsumeWagerResult, error) {
	receivedAt := d.Now().UTC().Truncate(time.Microsecond)
	outcome, err := d.Process.ProcessWager(ctx, input.Wager)
	if err != nil {
		// Contract failures never commit and can never succeed on redelivery.
		if errors.Is(err, ErrInvalidInput) || errors.Is(err, ErrInvalidKind) || errors.Is(err, ErrCurrencyNotAccepted) ||
			errors.Is(err, ports.ErrIdempotencyConflict) || errors.Is(err, wagering.ErrInvalidKind) ||
			errors.Is(err, wagering.ErrInvalidAmount) || errors.Is(err, wagering.ErrInvalidReference) ||
			errors.Is(err, wagering.ErrWalletScope) {
			return ConsumeWagerResult{}, fmt.Errorf("%w: %w", ErrPermanentMessage, err)
		}
		return ConsumeWagerResult{}, err
	}
	now := d.Now().UTC().Truncate(time.Microsecond)
	replayed, err := d.Inbox.TryCompleteInbox(ctx, ports.InboxCompletion{
		ConsumerName:  input.ConsumerName,
		MessageID:     input.MessageID,
		PayloadHash:   input.PayloadHash,
		TransactionID: outcome.TransactionID,
		ReceivedAt:    receivedAt,
		CompletedAt:   now,
	})
	if err != nil {
		if errors.Is(err, ports.ErrInboxConflict) {
			return ConsumeWagerResult{}, fmt.Errorf("%w: %w", ErrPermanentMessage, err)
		}
		return ConsumeWagerResult{}, err
	}
	return ConsumeWagerResult{
		TransactionID: outcome.TransactionID,
		Status:        outcome.Status,
		FailureCode:   outcome.FailureCode,
		Replay:        outcome.IdempotentReplay,
		InboxReplay:   replayed,
	}, nil
}

func (d ConsumeWagerDeps) validate() error {
	if d.UnitOfWork == nil || d.Inbox == nil || d.Now == nil {
		return ErrIncompleteDeps
	}
	// The nested ProcessWager validates its own dependencies.
	return nil
}
