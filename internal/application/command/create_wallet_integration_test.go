//go:build integration

// These tests exercise the whole opening use case against real PostgreSQL with
// the migrations applied: no mocks, no in-memory substitutes.
package command_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hendelsantos/jungle-backend-challenge-go/internal/adapters/postgres"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/command"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/ports"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/ledger"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/money"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/testsupport"
)

var openingTime = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

var errInjected = errors.New("injected ledger failure")

type failingLedger struct {
	ports.LedgerRepository
}

func (failingLedger) InsertLedgerEntry(context.Context, ledger.Entry) error { return errInjected }

func newDeps(t *testing.T) (command.CreateWalletDeps, *pgxpool.Pool) {
	t.Helper()
	pool := testsupport.PostgresPoolOrSkip(t)
	return command.CreateWalletDeps{
		UnitOfWork: postgres.NewUnitOfWork(pool),
		Wallets:    postgres.NewWalletRepository(),
		Wagers:     postgres.NewWagerTransactionRepository(),
		Ledger:     postgres.NewLedgerRepository(),
		Outbox:     postgres.NewOutboxRepository(),
		Now:        func() time.Time { return openingTime },
		NewID:      uuid.New,
	}, pool
}

func parseBRL(t *testing.T, amount string) money.Money {
	t.Helper()
	value, err := money.ParseDecimal(amount, money.BRL)
	if err != nil {
		t.Fatalf("ParseDecimal(%q): %v", amount, err)
	}
	return value
}

func queryInt(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var value int64
	if err := pool.QueryRow(ctx, sql, args...).Scan(&value); err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return value
}

func TestCreateWalletPositiveOpeningIntegration(t *testing.T) {
	deps, pool := newDeps(t)
	playerID := uuid.New()

	result, err := deps.CreateWallet(context.Background(), command.CreateWalletInput{
		PlayerID:       playerID,
		InitialBalance: parseBRL(t, "1000.00"),
		CorrelationID:  uuid.New(),
	})
	if err != nil {
		t.Fatalf("CreateWallet(): %v", err)
	}
	if result.Version != 1 || result.Balance.MinorUnits() != 100000 {
		t.Fatalf("unexpected result: %+v", result)
	}

	if got := queryInt(t, pool, `SELECT balance_cents FROM wallets WHERE id = $1`, result.WalletID); got != 100000 {
		t.Fatalf("wallet balance: got %d, want 100000", got)
	}
	if got := queryInt(t, pool, `SELECT version FROM wallets WHERE id = $1`, result.WalletID); got != 1 {
		t.Fatalf("wallet version: got %d, want 1", got)
	}

	openingCount := queryInt(t, pool, `
		SELECT count(*) FROM wager_transactions
		WHERE wallet_id = $1 AND kind = 'OPENING' AND status = 'PROCESSED'
		  AND amount_cents = 100000 AND result_balance_cents = 100000 AND result_wallet_version = 1`,
		result.WalletID)
	if openingCount != 1 {
		t.Fatalf("processed opening rows: got %d, want 1", openingCount)
	}

	ledgerCount := queryInt(t, pool, `
		SELECT count(*) FROM wallet_ledger_entries
		WHERE wallet_id = $1 AND direction = 'CREDIT' AND amount_cents = 100000
		  AND balance_before_cents = 0 AND balance_after_cents = 100000`,
		result.WalletID)
	if ledgerCount != 1 {
		t.Fatalf("ledger entries: got %d, want 1", ledgerCount)
	}

	eventCount := queryInt(t, pool, `
		SELECT count(*) FROM outbox_events
		WHERE ((aggregate_id = $1 AND event_type = 'WalletBalanceChanged')
		    OR (aggregate_id = (SELECT id FROM wager_transactions WHERE wallet_id = $1)
		        AND event_type = 'WagerTransactionProcessed'))
		  AND published_at IS NULL`,
		result.WalletID)
	if eventCount != 2 {
		t.Fatalf("outbox events: got %d, want 2 (one per required type, unpublished)", eventCount)
	}
}

func TestCreateWalletZeroIntegration(t *testing.T) {
	deps, pool := newDeps(t)
	playerID := uuid.New()

	result, err := deps.CreateWallet(context.Background(), command.CreateWalletInput{
		PlayerID:       playerID,
		InitialBalance: parseBRL(t, "0.00"),
		CorrelationID:  uuid.New(),
	})
	if err != nil {
		t.Fatalf("CreateWallet(): %v", err)
	}
	if got := queryInt(t, pool, `SELECT balance_cents FROM wallets WHERE id = $1`, result.WalletID); got != 0 {
		t.Fatalf("wallet balance: got %d, want 0", got)
	}
	if got := queryInt(t, pool, `SELECT count(*) FROM wager_transactions WHERE wallet_id = $1`, result.WalletID); got != 0 {
		t.Fatalf("zero opening must not create transactions: got %d", got)
	}
	if got := queryInt(t, pool, `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1`, result.WalletID); got != 0 {
		t.Fatalf("zero opening must not create ledger entries: got %d", got)
	}
	if got := queryInt(t, pool, `SELECT count(*) FROM outbox_events WHERE aggregate_id = $1`, result.WalletID); got != 0 {
		t.Fatalf("zero opening must not create events: got %d", got)
	}
}

func TestCreateWalletDuplicateIntegration(t *testing.T) {
	deps, pool := newDeps(t)
	playerID := uuid.New()
	input := command.CreateWalletInput{
		PlayerID:       playerID,
		InitialBalance: parseBRL(t, "500.00"),
		CorrelationID:  uuid.New(),
	}
	if _, err := deps.CreateWallet(context.Background(), input); err != nil {
		t.Fatalf("first CreateWallet(): %v", err)
	}

	_, err := deps.CreateWallet(context.Background(), command.CreateWalletInput{
		PlayerID:       playerID,
		InitialBalance: parseBRL(t, "999.00"),
		CorrelationID:  uuid.New(),
	})
	if !errors.Is(err, ports.ErrWalletAlreadyExists) {
		t.Fatalf("second CreateWallet(): got %v, want ErrWalletAlreadyExists", err)
	}

	if got := queryInt(t, pool, `SELECT count(*) FROM wallets WHERE player_id = $1`, playerID); got != 1 {
		t.Fatalf("duplicate opening must leave a single wallet: got %d", got)
	}
	if got := queryInt(t, pool, `SELECT count(*) FROM wager_transactions WHERE wallet_id = (SELECT id FROM wallets WHERE player_id = $1)`, playerID); got != 1 {
		t.Fatalf("duplicate opening must not add transactions: got %d", got)
	}
	if got := queryInt(t, pool, `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = (SELECT id FROM wallets WHERE player_id = $1)`, playerID); got != 1 {
		t.Fatalf("duplicate opening must not add ledger entries: got %d", got)
	}
}

func TestCreateWalletMidTransactionFailureRollsBackEverythingIntegration(t *testing.T) {
	deps, pool := newDeps(t)
	deps.Ledger = failingLedger{}
	playerID := uuid.New()

	_, err := deps.CreateWallet(context.Background(), command.CreateWalletInput{
		PlayerID:       playerID,
		InitialBalance: parseBRL(t, "1000.00"),
		CorrelationID:  uuid.New(),
	})
	if !errors.Is(err, errInjected) {
		t.Fatalf("CreateWallet(): got %v, want the injected failure", err)
	}
	// The wallet insert succeeded inside the transaction, but the failed ledger
	// write must roll the whole unit of work back: nothing may persist.
	if got := queryInt(t, pool, `SELECT count(*) FROM wallets WHERE player_id = $1`, playerID); got != 0 {
		t.Fatalf("failed opening must not persist the wallet: got %d", got)
	}
	if got := queryInt(t, pool, `SELECT count(*) FROM wager_transactions WHERE player_id = $1`, playerID); got != 0 {
		t.Fatalf("failed opening must not persist transactions: got %d", got)
	}
	if got := queryInt(t, pool, `SELECT count(*) FROM outbox_events WHERE payload_json->'data'->>'playerId' = $1`, playerID.String()); got != 0 {
		t.Fatalf("failed opening must not persist events: got %d", got)
	}
}

func TestCreateWalletPreservesPlayerWalletOnIndependentPlayersIntegration(t *testing.T) {
	deps, _ := newDeps(t)
	first, err := deps.CreateWallet(context.Background(), command.CreateWalletInput{
		PlayerID:       uuid.New(),
		InitialBalance: parseBRL(t, "100.00"),
		CorrelationID:  uuid.New(),
	})
	if err != nil {
		t.Fatalf("first CreateWallet(): %v", err)
	}
	second, err := deps.CreateWallet(context.Background(), command.CreateWalletInput{
		PlayerID:       uuid.New(),
		InitialBalance: parseBRL(t, "200.00"),
		CorrelationID:  uuid.New(),
	})
	if err != nil {
		t.Fatalf("second CreateWallet(): %v", err)
	}
	if first.WalletID == second.WalletID {
		t.Fatal("independent players must receive independent wallets")
	}
}
