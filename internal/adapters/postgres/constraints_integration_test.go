//go:build integration

// Automated verification of the schema protections that were previously only
// checked during the migration review: the append-only triggers and the
// eliminatory balance constraint, using the runtime credentials of the test
// database.
package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hendelsantos/jungle-backend-challenge-go/internal/testsupport"
)

func mustExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func TestLedgerAppendOnlyTriggersIntegration(t *testing.T) {
	pool := testsupport.PostgresPoolOrSkip(t)
	at := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	walletID, playerID, transactionID, entryID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	mustExec(t, pool, `
		INSERT INTO wallets (id, player_id, currency, balance_cents, version, created_at, updated_at)
		VALUES ($1, $2, 'BRL', 100, 1, $3, $3)`, walletID, playerID, at)
	mustExec(t, pool, `
		INSERT INTO wager_transactions (id, wallet_id, player_id, kind, status, amount_cents, currency, created_at, updated_at)
		VALUES ($1, $2, $3, 'OPENING', 'PENDING', 0, 'BRL', $4, $4)`, transactionID, walletID, playerID, at)
	mustExec(t, pool, `
		INSERT INTO wallet_ledger_entries (id, wallet_id, transaction_id, direction, amount_cents, balance_before_cents, balance_after_cents, created_at)
		VALUES ($1, $2, $3, 'CREDIT', 100, 0, 100, $4)`, entryID, walletID, transactionID, at)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mutations := map[string]string{
		"update":   "UPDATE wallet_ledger_entries SET amount_cents = 1",
		"delete":   "DELETE FROM wallet_ledger_entries",
		"truncate": "TRUNCATE TABLE wallet_ledger_entries",
	}
	for name, sql := range mutations {
		t.Run(name, func(t *testing.T) {
			_, err := pool.Exec(ctx, sql)
			if err == nil {
				t.Fatalf("%s on the ledger was accepted", name)
			}
			if !strings.Contains(err.Error(), "append-only") {
				t.Fatalf("%s: unexpected error: %v", name, err)
			}
		})
	}

	// The entry must still exist untouched after the rejected mutations.
	var amount int64
	if err := pool.QueryRow(ctx, "SELECT amount_cents FROM wallet_ledger_entries WHERE id = $1", entryID).Scan(&amount); err != nil {
		t.Fatalf("entry disappeared: %v", err)
	}
	if amount != 100 {
		t.Fatalf("entry was modified: %d", amount)
	}
}

func TestSchemaConstraintRejectsNegativeBalanceIntegration(t *testing.T) {
	pool := testsupport.PostgresPoolOrSkip(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := pool.Exec(ctx, `
		INSERT INTO wallets (id, player_id, currency, balance_cents, version, created_at, updated_at)
		VALUES ($1, $2, 'BRL', -1, 1, now(), now())`, uuid.New(), uuid.New())
	if err == nil {
		t.Fatal("negative balance was accepted")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("expected check violation (23514), got: %v", err)
	}
}
