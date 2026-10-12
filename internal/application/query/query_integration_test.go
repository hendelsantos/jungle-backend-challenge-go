//go:build integration

// Read-path integration against real PostgreSQL with the migrations applied
// by the shared harness; no mocks anywhere.
package query_test

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
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/query"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/money"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/testsupport"
)

var baseTime = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

func newReadDeps(t *testing.T) (query.GetWalletDeps, query.ListLedgerDeps, query.ReconcileWalletDeps, *pgxpool.Pool) {
	t.Helper()
	pool := testsupport.PostgresPoolOrSkip(t)
	walletReader := postgres.NewWalletReader(pool)
	return query.GetWalletDeps{Wallets: walletReader},
		query.ListLedgerDeps{Wallets: walletReader, Ledger: postgres.NewLedgerReader(pool)},
		query.ReconcileWalletDeps{Reconciliation: postgres.NewReconciler(pool)},
		pool
}

func exec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

// insertWalletRow inserts a wallet directly, allowing divergence scenarios
// the use case would never produce.
func insertWalletRow(t *testing.T, pool *pgxpool.Pool, balanceCents int64) uuid.UUID {
	t.Helper()
	walletID, playerID := uuid.New(), uuid.New()
	exec(t, pool, `
		INSERT INTO wallets (id, player_id, currency, balance_cents, version, created_at, updated_at)
		VALUES ($1, $2, 'BRL', $3, 1, $4, $4)`, walletID, playerID, balanceCents, baseTime)
	return walletID
}

func insertOpeningRow(t *testing.T, pool *pgxpool.Pool, walletID, transactionID uuid.UUID) {
	t.Helper()
	exec(t, pool, `
		INSERT INTO wager_transactions (id, wallet_id, player_id, kind, status, amount_cents, currency, created_at, updated_at)
		VALUES ($1, $2, (SELECT player_id FROM wallets WHERE id = $2), 'OPENING', 'PENDING', 0, 'BRL', $3, $3)`,
		transactionID, walletID, baseTime)
}

// insertPendingWinRow satisfies the ledger foreign key without colliding with
// the one-OPENING-per-wallet uniqueness.
func insertPendingWinRow(t *testing.T, pool *pgxpool.Pool, walletID, transactionID uuid.UUID) {
	t.Helper()
	exec(t, pool, `
		INSERT INTO wager_transactions (
			id, wallet_id, player_id, kind, status, amount_cents, currency,
			provider_id, external_transaction_id, idempotency_key, payload_hash, round_id, game_id,
			created_at, updated_at)
		VALUES ($1, $2, (SELECT player_id FROM wallets WHERE id = $2), 'WIN', 'PENDING', 100, 'BRL',
			'test-provider', $3, $3, repeat('ab', 32), 'round-1', 'game-1', $4, $4)`,
		transactionID, walletID, "tx-"+transactionID.String(), baseTime)
}

func insertEntryRow(t *testing.T, pool *pgxpool.Pool, entryID, walletID, transactionID uuid.UUID, amount, before, after int64, at time.Time) {
	t.Helper()
	exec(t, pool, `
		INSERT INTO wallet_ledger_entries (id, wallet_id, transaction_id, direction, amount_cents, balance_before_cents, balance_after_cents, created_at)
		VALUES ($1, $2, $3, 'CREDIT', $4, $5, $6, $7)`,
		entryID, walletID, transactionID, amount, before, after, at)
}

// sortedPair returns two fresh uuids with a < b, so a same-timestamp pair has
// a deterministic (created_at, id) tie-break while staying unique per run:
// the harness never resets data and ledger rows cannot be deleted.
func sortedPair() (uuid.UUID, uuid.UUID) {
	a, b := uuid.New(), uuid.New()
	if a.String() > b.String() {
		a, b = b, a
	}
	return a, b
}

func TestGetWalletIntegration(t *testing.T) {
	getWallet, _, _, pool := newReadDeps(t)
	create := command.CreateWalletDeps{
		UnitOfWork: postgres.NewUnitOfWork(pool),
		Wallets:    postgres.NewWalletRepository(),
		Wagers:     postgres.NewWagerTransactionRepository(),
		Ledger:     postgres.NewLedgerRepository(),
		Outbox:     postgres.NewOutboxRepository(),
		Now:        func() time.Time { return baseTime },
		NewID:      uuid.New,
	}
	balance, err := money.ParseDecimal("250.00", money.BRL)
	if err != nil {
		t.Fatal(err)
	}
	created, err := create.CreateWallet(context.Background(), command.CreateWalletInput{
		PlayerID: uuid.New(), InitialBalance: balance, CorrelationID: uuid.New(),
	})
	if err != nil {
		t.Fatalf("CreateWallet(): %v", err)
	}

	state, err := getWallet.GetWallet(context.Background(), created.WalletID)
	if err != nil {
		t.Fatalf("GetWallet(): %v", err)
	}
	if state.ID != created.WalletID || state.Version != 1 || state.Balance != balance {
		t.Fatalf("unexpected state: %+v", state)
	}

	if _, err := getWallet.GetWallet(context.Background(), uuid.New()); !errors.Is(err, ports.ErrWalletNotFound) {
		t.Fatalf("GetWallet(unknown): got %v, want ErrWalletNotFound", err)
	}
}

func TestListLedgerPaginationIntegration(t *testing.T) {
	_, listLedger, _, pool := newReadDeps(t)
	walletID := insertWalletRow(t, pool, 0)
	entryA, entryB := sortedPair()
	entryC := uuid.New()
	transactionA, transactionB, transactionC := uuid.New(), uuid.New(), uuid.New()
	for _, transactionID := range []uuid.UUID{transactionA, transactionB, transactionC} {
		insertPendingWinRow(t, pool, walletID, transactionID)
	}
	// Two entries share created_at: the uuid tie-break must order them.
	insertEntryRow(t, pool, entryA, walletID, transactionA, 100, 0, 100, baseTime)
	insertEntryRow(t, pool, entryB, walletID, transactionB, 100, 100, 200, baseTime)
	insertEntryRow(t, pool, entryC, walletID, transactionC, 100, 200, 300, baseTime.Add(time.Second))

	page, err := listLedger.ListLedger(context.Background(), walletID, "", 2)
	if err != nil {
		t.Fatalf("ListLedger(): %v", err)
	}
	if len(page.Entries) != 2 || page.NextCursor == "" {
		t.Fatalf("first page: %d entries, cursor %q", len(page.Entries), page.NextCursor)
	}
	if page.Entries[0].ID() != entryA || page.Entries[1].ID() != entryB {
		t.Fatalf("tie-break order: %v then %v", page.Entries[0].ID(), page.Entries[1].ID())
	}

	next, err := listLedger.ListLedger(context.Background(), walletID, page.NextCursor, 2)
	if err != nil {
		t.Fatalf("ListLedger(cursor): %v", err)
	}
	if len(next.Entries) != 1 || next.Entries[0].ID() != entryC || next.NextCursor != "" {
		t.Fatalf("second page: %d entries, cursor %q", len(next.Entries), next.NextCursor)
	}

	if _, err := listLedger.ListLedger(context.Background(), walletID, "not-a-cursor", 10); !errors.Is(err, ports.ErrInvalidCursor) {
		t.Fatalf("ListLedger(invalid cursor): got %v, want ErrInvalidCursor", err)
	}
	if _, err := listLedger.ListLedger(context.Background(), uuid.New(), "", 10); !errors.Is(err, ports.ErrWalletNotFound) {
		t.Fatalf("ListLedger(missing wallet): got %v, want ErrWalletNotFound", err)
	}

	// Walking with the returned cursors must terminate: the final page may
	// carry entries, but never produces another cursor.
	lastCursor := page.NextCursor
	pages := 0
	for {
		current, err := listLedger.ListLedger(context.Background(), walletID, lastCursor, 2)
		if err != nil {
			t.Fatalf("ListLedger(walk): %v", err)
		}
		pages++
		if len(current.Entries) > 2 {
			t.Fatalf("page exceeds the limit: %d", len(current.Entries))
		}
		if current.NextCursor == "" {
			break
		}
		lastCursor = current.NextCursor
	}
	if pages != 1 {
		t.Fatalf("expected a single following page, walked %d", pages)
	}
}

func TestReconcileWalletIntegration(t *testing.T) {
	_, _, reconcile, pool := newReadDeps(t)

	t.Run("consistent opening", func(t *testing.T) {
		create := command.CreateWalletDeps{
			UnitOfWork: postgres.NewUnitOfWork(pool),
			Wallets:    postgres.NewWalletRepository(),
			Wagers:     postgres.NewWagerTransactionRepository(),
			Ledger:     postgres.NewLedgerRepository(),
			Outbox:     postgres.NewOutboxRepository(),
			Now:        func() time.Time { return baseTime },
			NewID:      uuid.New,
		}
		balance, err := money.ParseDecimal("1000.00", money.BRL)
		if err != nil {
			t.Fatal(err)
		}
		created, err := create.CreateWallet(context.Background(), command.CreateWalletInput{
			PlayerID: uuid.New(), InitialBalance: balance, CorrelationID: uuid.New(),
		})
		if err != nil {
			t.Fatal(err)
		}
		report, err := reconcile.ReconcileWallet(context.Background(), created.WalletID)
		if err != nil {
			t.Fatalf("ReconcileWallet(): %v", err)
		}
		if !report.Consistent || report.Difference.MinorUnits() != 0 ||
			report.StoredBalance != balance || report.CalculatedBalance != balance || report.CheckedEntries != 1 {
			t.Fatalf("unexpected report: %+v", report)
		}
	})

	t.Run("zero wallet with empty ledger", func(t *testing.T) {
		walletID := insertWalletRow(t, pool, 0)
		report, err := reconcile.ReconcileWallet(context.Background(), walletID)
		if err != nil {
			t.Fatalf("ReconcileWallet(): %v", err)
		}
		if !report.Consistent || report.CheckedEntries != 0 || report.StoredBalance.MinorUnits() != 0 {
			t.Fatalf("unexpected report: %+v", report)
		}
	})

	t.Run("divergence is reported without repairs", func(t *testing.T) {
		walletID := insertWalletRow(t, pool, 50000)
		transactionID := uuid.New()
		insertOpeningRow(t, pool, walletID, transactionID)
		insertEntryRow(t, pool, uuid.New(), walletID, transactionID, 40000, 0, 40000, baseTime)

		report, err := reconcile.ReconcileWallet(context.Background(), walletID)
		if err != nil {
			t.Fatalf("ReconcileWallet(): %v", err)
		}
		if report.Consistent {
			t.Fatal("stored and calculated diverge: the report must not be consistent")
		}
		if report.StoredBalance.MinorUnits() != 50000 ||
			report.CalculatedBalance.MinorUnits() != 40000 ||
			report.Difference.MinorUnits() != 10000 || report.CheckedEntries != 1 {
			t.Fatalf("unexpected report: %+v", report)
		}
		var stored int64
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := pool.QueryRow(ctx, "SELECT balance_cents FROM wallets WHERE id = $1", walletID).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if stored != 50000 {
			t.Fatalf("reconciliation altered the balance: %d", stored)
		}
	})

	t.Run("missing wallet", func(t *testing.T) {
		if _, err := reconcile.ReconcileWallet(context.Background(), uuid.New()); !errors.Is(err, ports.ErrWalletNotFound) {
			t.Fatalf("got %v, want ErrWalletNotFound", err)
		}
	})
}
