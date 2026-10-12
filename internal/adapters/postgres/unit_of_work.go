package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/ports"
)

// Compile-time proof that the adapter satisfies the application contract.
var _ ports.UnitOfWork = (*UnitOfWork)(nil)

type txKey struct{}

// UnitOfWork runs use cases inside one pgx transaction. The active transaction
// travels in the context; repositories in this package obtain it through
// TxFromContext, so every write of a use case shares a single commit.
type UnitOfWork struct {
	pool *pgxpool.Pool
}

func NewUnitOfWork(pool *pgxpool.Pool) *UnitOfWork {
	return &UnitOfWork{pool: pool}
}

func (u *UnitOfWork) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if _, ok := TxFromContext(ctx); ok {
		// Join the surrounding transaction: nested work must not commit alone.
		return fn(ctx)
	}
	tx, err := u.pool.Begin(ctx)
	if err != nil {
		return markRetryable(fmt.Errorf("postgres: begin transaction: %w", err))
	}
	if err := fn(context.WithValue(ctx, txKey{}, tx)); err != nil {
		// Rollback must run even when ctx was cancelled; keep the original error.
		if rollbackErr := tx.Rollback(context.WithoutCancel(ctx)); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			return markRetryable(errors.Join(err, fmt.Errorf("postgres: rollback: %w", rollbackErr)))
		}
		return markRetryable(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return markRetryable(fmt.Errorf("postgres: commit transaction: %w", err))
	}
	return nil
}

// markRetryable classifies deadlocks, serialization failures and idempotency
// races so the application can retry the whole unit of work without knowing
// the driver. A concurrent duplicate on an idempotency index is retryable:
// the next attempt resolves the committed operation and returns a replay.
func markRetryable(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch pgErr.Code {
	case "40P01", "40001": // deadlock detected, serialization failure
		return fmt.Errorf("%w: %w", ports.ErrRetryableTransaction, err)
	case "23505": // unique violation
		if pgErr.ConstraintName == "wager_transactions_provider_idempotency_uidx" ||
			pgErr.ConstraintName == "wager_transactions_provider_external_uidx" {
			return fmt.Errorf("%w: %w", ports.ErrRetryableTransaction, err)
		}
	}
	return err
}

// TxFromContext exposes the transaction bound by WithinTx to repositories in
// this package. Callers outside the adapter must go through the port instead.
func TxFromContext(ctx context.Context) (pgx.Tx, bool) {
	tx, ok := ctx.Value(txKey{}).(pgx.Tx)
	return tx, ok
}
