package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/ports"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/money"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wagering"
)

var _ ports.WagerTransactionReader = (*WagerTransactionReader)(nil)

// WagerTransactionReader resolves external operations. The use case calls it
// inside its unit of work; reads join the active transaction when present.
type WagerTransactionReader struct {
	pool *pgxpool.Pool
}

func NewWagerTransactionReader(pool *pgxpool.Pool) *WagerTransactionReader {
	return &WagerTransactionReader{pool: pool}
}

const wagerTransactionSelect = `
	SELECT id, wallet_id, player_id, kind, status, amount_cents, currency,
	       provider_id, external_transaction_id, idempotency_key, payload_hash,
	       round_id, game_id, reference_external_transaction_id, reference_transaction_id,
	       failure_code, result_balance_cents, result_wallet_version, created_at, updated_at
	FROM wager_transactions`

func (r *WagerTransactionReader) FindByIdempotencyKey(ctx context.Context, providerID, idempotencyKey string) (wagering.State, error) {
	return r.findOne(ctx, `WHERE provider_id = $1 AND idempotency_key = $2`, providerID, idempotencyKey)
}

func (r *WagerTransactionReader) FindByExternalID(ctx context.Context, providerID, externalTransactionID string) (wagering.State, error) {
	return r.findOne(ctx, `WHERE provider_id = $1 AND external_transaction_id = $2`, providerID, externalTransactionID)
}

func (r *WagerTransactionReader) FindReference(ctx context.Context, providerID, referenceExternalTransactionID string) (ports.ResolvedReference, error) {
	state, err := r.findOne(ctx, `WHERE provider_id = $1 AND external_transaction_id = $2`, providerID, referenceExternalTransactionID)
	if err != nil {
		return ports.ResolvedReference{}, err
	}
	// ADR 0001: any processed direct compensation of the referenced operation
	// blocks the next one, whether it was a REFUND or a ROLLBACK.
	var compensated bool
	err = executorFor(ctx, r.pool).QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM wager_transactions c
			WHERE c.reference_transaction_id = $1
			  AND c.kind IN ('REFUND', 'ROLLBACK')
			  AND c.status = 'PROCESSED'
		)`, state.Request.ID).Scan(&compensated)
	if err != nil {
		return ports.ResolvedReference{}, fmt.Errorf("postgres: check direct compensation: %w", err)
	}
	return ports.ResolvedReference{State: state, Compensated: compensated}, nil
}

func (r *WagerTransactionReader) findOne(ctx context.Context, where string, args ...any) (wagering.State, error) {
	row := executorFor(ctx, r.pool).QueryRow(ctx, wagerTransactionSelect+" "+where, args...)
	state, err := scanWagerTransaction(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return wagering.State{}, ports.ErrWagerTransactionNotFound
	}
	return state, err
}

type rowScanner interface {
	Scan(dest ...any) error
}

// scanWagerTransaction maps one row into domain state and revalidates it
// through Rehydrate: a corrupted row is an error, never accepted silently.
func scanWagerTransaction(row rowScanner) (wagering.State, error) {
	var (
		state                                      wagering.State
		kind, status, currency                     string
		amountCents                                int64
		provider, external, key, hash, round, game *string
		refExt, failureCode                        *string
		referenceID                                *uuid.UUID
		resultBalance, resultVersion               *int64
	)
	err := row.Scan(
		&state.Request.ID, &state.Request.WalletID, &state.Request.PlayerID,
		&kind, &status, &amountCents, &currency,
		&provider, &external, &key, &hash, &round, &game, &refExt,
		&referenceID, &failureCode, &resultBalance, &resultVersion,
		&state.CreatedAt, &state.UpdatedAt,
	)
	if err != nil {
		return wagering.State{}, fmt.Errorf("postgres: scan wager transaction: %w", err)
	}
	amount, err := money.FromMinorUnits(amountCents, currency)
	if err != nil {
		return wagering.State{}, fmt.Errorf("postgres: wager amount: %w", err)
	}
	state.Request.Money = amount
	state.Request.Kind = wagering.Kind(kind)
	state.Status = wagering.Status(status)
	state.Request.ProviderID = deref(provider)
	state.Request.ExternalTransactionID = deref(external)
	state.Request.IdempotencyKey = deref(key)
	state.Request.PayloadHash = deref(hash)
	state.Request.RoundID = deref(round)
	state.Request.GameID = deref(game)
	state.Request.ReferenceExternalTransactionID = deref(refExt)
	state.FailureCode = wagering.FailureCode(deref(failureCode))
	if referenceID != nil {
		state.ReferenceID = *referenceID
	}
	if resultBalance != nil && resultVersion != nil {
		balance, err := money.FromMinorUnits(*resultBalance, currency)
		if err != nil {
			return wagering.State{}, fmt.Errorf("postgres: wager result balance: %w", err)
		}
		state.HasResult = true
		state.Result = wagering.Result{Balance: balance, WalletVersion: *resultVersion}
	}
	rehydrated, err := wagering.Rehydrate(state)
	if err != nil {
		return wagering.State{}, fmt.Errorf("postgres: wager transaction violates invariants: %w", err)
	}
	return rehydrated.Snapshot(), nil
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
