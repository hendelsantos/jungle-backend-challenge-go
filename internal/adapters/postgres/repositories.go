package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/ports"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/events"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/ledger"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/money"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wagering"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wallet"
)

// uniqueViolation is PostgreSQL SQLSTATE 23505.
const uniqueViolation = "23505"

var (
	_ ports.WalletRepository           = (*WalletRepository)(nil)
	_ ports.WagerTransactionRepository = (*WagerTransactionRepository)(nil)
	_ ports.LedgerRepository           = (*LedgerRepository)(nil)
	_ ports.OutboxRepository           = (*OutboxRepository)(nil)
)

// activeTx rejects writes attempted outside a UnitOfWork transaction.
func activeTx(ctx context.Context, operation string) (pgx.Tx, error) {
	tx, ok := TxFromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("postgres: %s requires an active UnitOfWork transaction", operation)
	}
	return tx, nil
}

type WalletRepository struct{}

func NewWalletRepository() *WalletRepository { return &WalletRepository{} }

func (WalletRepository) InsertWallet(ctx context.Context, state wallet.State) error {
	tx, err := activeTx(ctx, "insert wallet")
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO wallets (id, player_id, currency, balance_cents, version, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		state.ID, state.PlayerID, state.Balance.Currency(), state.Balance.MinorUnits(),
		state.Version, state.CreatedAt, state.UpdatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation &&
			pgErr.ConstraintName == "wallets_player_currency_uidx" {
			return ports.ErrWalletAlreadyExists
		}
		return fmt.Errorf("postgres: insert wallet: %w", err)
	}
	return nil
}

// LockWallet serializes writers per wallet row: concurrent operations on the
// same wallet block on FOR UPDATE while different wallets proceed in parallel.
func (WalletRepository) LockWallet(ctx context.Context, id uuid.UUID) (wallet.State, error) {
	tx, err := activeTx(ctx, "lock wallet")
	if err != nil {
		return wallet.State{}, err
	}
	var (
		state        wallet.State
		currency     string
		balanceCents int64
	)
	err = tx.QueryRow(ctx, `
		SELECT id, player_id, currency, balance_cents, version, created_at, updated_at
		FROM wallets
		WHERE id = $1
		FOR UPDATE`, id).
		Scan(&state.ID, &state.PlayerID, &currency, &balanceCents, &state.Version, &state.CreatedAt, &state.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return wallet.State{}, ports.ErrWalletNotFound
	}
	if err != nil {
		return wallet.State{}, fmt.Errorf("postgres: lock wallet: %w", err)
	}
	balance, err := money.FromMinorUnits(balanceCents, currency)
	if err != nil {
		return wallet.State{}, fmt.Errorf("postgres: wallet %s balance: %w", id, err)
	}
	state.Balance = balance
	rehydrated, err := wallet.Rehydrate(state)
	if err != nil {
		return wallet.State{}, fmt.Errorf("postgres: wallet %s violates invariants: %w", id, err)
	}
	return rehydrated.Snapshot(), nil
}

// UpdateWalletBalance writes the new balance only when the row still carries
// the version the use case observed; under the row lock this is a defense
// against lost updates, not an expected path.
func (WalletRepository) UpdateWalletBalance(ctx context.Context, state wallet.State, expectedVersion int64) error {
	tx, err := activeTx(ctx, "update wallet balance")
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE wallets
		SET balance_cents = $1, version = $2, updated_at = $3
		WHERE id = $4 AND version = $5`,
		state.Balance.MinorUnits(), state.Version, state.UpdatedAt, state.ID, expectedVersion)
	if err != nil {
		return fmt.Errorf("postgres: update wallet balance: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("postgres: wallet %s: %w (expected version %d)",
			state.ID, ports.ErrWalletConcurrentModification, expectedVersion)
	}
	return nil
}

type WagerTransactionRepository struct{}

func NewWagerTransactionRepository() *WagerTransactionRepository {
	return &WagerTransactionRepository{}
}

func (WagerTransactionRepository) InsertWagerTransaction(ctx context.Context, state wagering.State) error {
	tx, err := activeTx(ctx, "insert wager transaction")
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO wager_transactions (
			id, wallet_id, player_id, kind, status, amount_cents, currency,
			provider_id, external_transaction_id, idempotency_key, payload_hash, round_id, game_id,
			reference_external_transaction_id, reference_transaction_id, failure_code,
			result_balance_cents, result_wallet_version, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7,
			$8, $9, $10, $11, $12, $13,
			$14, $15, $16,
			$17, $18, $19, $20
		)`,
		state.Request.ID, state.Request.WalletID, state.Request.PlayerID,
		string(state.Request.Kind), string(state.Status),
		state.Request.Money.MinorUnits(), state.Request.Money.Currency(),
		nullText(state.Request.ProviderID),
		nullText(state.Request.ExternalTransactionID),
		nullText(state.Request.IdempotencyKey),
		nullText(state.Request.PayloadHash),
		nullText(state.Request.RoundID),
		nullText(state.Request.GameID),
		nullText(state.Request.ReferenceExternalTransactionID),
		nullUUID(state.ReferenceID),
		nullText(string(state.FailureCode)),
		nullInt64(state.Result.Balance.MinorUnits(), state.HasResult),
		nullInt64(state.Result.WalletVersion, state.HasResult),
		state.CreatedAt, state.UpdatedAt)
	if err != nil {
		return fmt.Errorf("postgres: insert wager transaction: %w", err)
	}
	return nil
}

type LedgerRepository struct{}

func NewLedgerRepository() *LedgerRepository { return &LedgerRepository{} }

func (LedgerRepository) InsertLedgerEntry(ctx context.Context, entry ledger.Entry) error {
	tx, err := activeTx(ctx, "insert ledger entry")
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO wallet_ledger_entries (
			id, wallet_id, transaction_id, direction, amount_cents,
			balance_before_cents, balance_after_cents, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		entry.ID(), entry.WalletID(), entry.TransactionID(), string(entry.Direction()),
		entry.Amount().MinorUnits(), entry.BalanceBefore().MinorUnits(),
		entry.BalanceAfter().MinorUnits(), entry.CreatedAt())
	if err != nil {
		return fmt.Errorf("postgres: insert ledger entry: %w", err)
	}
	return nil
}

type OutboxRepository struct{}

func NewOutboxRepository() *OutboxRepository { return &OutboxRepository{} }

func (OutboxRepository) InsertEvent(ctx context.Context, envelope events.Envelope) error {
	tx, err := activeTx(ctx, "insert outbox event")
	if err != nil {
		return err
	}
	payload, err := envelope.MarshalJSON()
	if err != nil {
		return fmt.Errorf("postgres: marshal outbox event: %w", err)
	}
	// The event is eligible for publishing immediately; attempts start at zero.
	_, err = tx.Exec(ctx, `
		INSERT INTO outbox_events (
			event_id, aggregate_id, event_type, payload_json, occurred_at, next_attempt_at, created_at
		) VALUES ($1, $2, $3, $4, $5, $5, $5)`,
		envelope.EventID(), envelope.AggregateID(), string(envelope.EventType()),
		payload, envelope.OccurredAt())
	if err != nil {
		return fmt.Errorf("postgres: insert outbox event: %w", err)
	}
	return nil
}

func nullText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullUUID(id uuid.UUID) any {
	if id == uuid.Nil {
		return nil
	}
	return id
}

func nullInt64(value int64, present bool) any {
	if !present {
		return nil
	}
	return value
}
