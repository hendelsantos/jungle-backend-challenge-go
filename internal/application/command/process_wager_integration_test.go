//go:build integration

// ProcessWager integration against real PostgreSQL with the migrations
// applied: replays, conflicts, the official 2x80-over-100 dispute (sequential
// and concurrent), refunds with single compensation and pending references.
package command_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hendelsantos/jungle-backend-challenge-go/internal/adapters/postgres"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/command"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/ports"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/events"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/money"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wagering"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/testsupport"
)

var wagerTime = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

func newWagerDeps(t *testing.T) (command.ProcessWagerDeps, command.CreateWalletDeps, *pgxpool.Pool) {
	t.Helper()
	pool := testsupport.PostgresPoolOrSkip(t)
	wagers := postgres.NewWagerTransactionRepository()
	deps := command.ProcessWagerDeps{
		UnitOfWork:   postgres.NewUnitOfWork(pool),
		Wallets:      postgres.NewWalletRepository(),
		Wagers:       wagers,
		WagerLookups: postgres.NewWagerTransactionReader(pool),
		Ledger:       postgres.NewLedgerRepository(),
		Outbox:       postgres.NewOutboxRepository(),
		Now:          func() time.Time { return wagerTime },
		NewID:        uuid.New,
		Backoff:      func(int) time.Duration { return 0 },
	}
	create := command.CreateWalletDeps{
		UnitOfWork: postgres.NewUnitOfWork(pool),
		Wallets:    postgres.NewWalletRepository(),
		Wagers:     postgres.NewWagerTransactionRepository(),
		Ledger:     postgres.NewLedgerRepository(),
		Outbox:     postgres.NewOutboxRepository(),
		Now:        func() time.Time { return wagerTime },
		NewID:      uuid.New,
	}
	return deps, create, pool
}

func openWallet(t *testing.T, create command.CreateWalletDeps, amount string) (uuid.UUID, uuid.UUID, money.Money) {
	t.Helper()
	balance, err := money.ParseDecimal(amount, money.BRL)
	if err != nil {
		t.Fatal(err)
	}
	playerID := uuid.New()
	created, err := create.CreateWallet(context.Background(), command.CreateWalletInput{
		PlayerID: playerID, InitialBalance: balance, CorrelationID: uuid.New(),
	})
	if err != nil {
		t.Fatalf("CreateWallet(%s): %v", amount, err)
	}
	return created.WalletID, playerID, balance
}

func operation(kind wagering.Kind, walletID, playerID uuid.UUID, amount string, suffix string) command.ProcessWagerInput {
	value, err := money.ParseDecimal(amount, money.BRL)
	if err != nil {
		panic(err)
	}
	external := "tx-" + suffix + "-" + walletID.String()
	return command.ProcessWagerInput{
		ProviderID:            "provider-a",
		ExternalTransactionID: external,
		IdempotencyKey:        "provider-a:" + external,
		PlayerID:              playerID,
		WalletID:              walletID,
		RoundID:               "round-1",
		GameID:                "fortune-chimp",
		Kind:                  kind,
		Money:                 value,
		CorrelationID:         uuid.New(),
	}
}

func countRows(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var count int64
	if err := pool.QueryRow(ctx, sql, args...).Scan(&count); err != nil {
		t.Fatalf("query: %v", err)
	}
	return count
}

func walletBalance(t *testing.T, pool *pgxpool.Pool, walletID uuid.UUID) (int64, int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var balance, version int64
	if err := pool.QueryRow(ctx, "SELECT balance_cents, version FROM wallets WHERE id = $1", walletID).Scan(&balance, &version); err != nil {
		t.Fatal(err)
	}
	return balance, version
}

func TestProcessWagerBetProcessedIntegration(t *testing.T) {
	deps, create, pool := newWagerDeps(t)
	walletID, playerID, _ := openWallet(t, create, "100.00")

	result, err := deps.ProcessWager(context.Background(), operation(wagering.Bet, walletID, playerID, "25.00", "bet-1"))
	if err != nil {
		t.Fatalf("ProcessWager(): %v", err)
	}
	if result.Status != wagering.Processed || result.IdempotentReplay ||
		!result.HasBalance || result.Balance.MinorUnits() != 7500 || result.WalletVersion != 2 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if balance, version := walletBalance(t, pool, walletID); balance != 7500 || version != 2 {
		t.Fatalf("wallet after BET: (%d, %d)", balance, version)
	}
	entries := countRows(t, pool, "SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1 AND direction = 'DEBIT'", walletID)
	if entries != 1 {
		t.Fatalf("debit entries: %d", entries)
	}
	processed := countRows(t, pool, `
		SELECT count(*) FROM outbox_events WHERE event_type = 'WagerTransactionProcessed'
		  AND payload_json->'data'->>'transactionId' = $1`, result.TransactionID.String())
	balanceEvents := countRows(t, pool, `
		SELECT count(*) FROM outbox_events WHERE event_type = 'WalletBalanceChanged'
		  AND payload_json->'data'->>'transactionId' = $1`, result.TransactionID.String())
	if processed != 1 || balanceEvents != 1 {
		t.Fatalf("events: processed %d, balance changed %d", processed, balanceEvents)
	}
}

func TestProcessWagerReplayHasNoNewEffectsIntegration(t *testing.T) {
	deps, create, pool := newWagerDeps(t)
	walletID, playerID, _ := openWallet(t, create, "100.00")
	input := operation(wagering.Bet, walletID, playerID, "25.00", "bet-1")

	first, err := deps.ProcessWager(context.Background(), input)
	if err != nil {
		t.Fatalf("first ProcessWager(): %v", err)
	}
	replay, err := deps.ProcessWager(context.Background(), input)
	if err != nil {
		t.Fatalf("replay ProcessWager(): %v", err)
	}
	if !replay.IdempotentReplay || replay.TransactionID != first.TransactionID ||
		replay.Balance.MinorUnits() != 7500 || replay.WalletVersion != 2 {
		t.Fatalf("unexpected replay: %+v", replay)
	}
	if balance, version := walletBalance(t, pool, walletID); balance != 7500 || version != 2 {
		t.Fatalf("replay changed the wallet: (%d, %d)", balance, version)
	}
	entries := countRows(t, pool, "SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1", walletID)
	if entries != 2 { // opening credit + single bet debit
		t.Fatalf("ledger entries after replay: %d, want 2", entries)
	}
	txRows := countRows(t, pool, "SELECT count(*) FROM wager_transactions WHERE wallet_id = $1", walletID)
	if txRows != 2 { // opening + single bet
		t.Fatalf("wager rows after replay: %d, want 2", txRows)
	}
}

func TestProcessWagerIdempotencyConflictsIntegration(t *testing.T) {
	deps, create, _ := newWagerDeps(t)
	walletID, playerID, _ := openWallet(t, create, "100.00")
	first := operation(wagering.Bet, walletID, playerID, "25.00", "bet-1")
	if _, err := deps.ProcessWager(context.Background(), first); err != nil {
		t.Fatal(err)
	}

	t.Run("same key different content", func(t *testing.T) {
		conflicting := operation(wagering.Bet, walletID, playerID, "30.00", "bet-1")
		conflicting.IdempotencyKey = first.IdempotencyKey
		if _, err := deps.ProcessWager(context.Background(), conflicting); !errors.Is(err, ports.ErrIdempotencyConflict) {
			t.Fatalf("got %v, want ErrIdempotencyConflict", err)
		}
	})

	t.Run("same external id under another key", func(t *testing.T) {
		hijack := operation(wagering.Bet, walletID, playerID, "25.00", "bet-1")
		hijack.IdempotencyKey = "provider-a:other-key"
		if _, err := deps.ProcessWager(context.Background(), hijack); !errors.Is(err, ports.ErrIdempotencyConflict) {
			t.Fatalf("got %v, want ErrIdempotencyConflict", err)
		}
	})
}

func TestProcessWagerTwoBetsOf80Over100Integration(t *testing.T) {
	deps, create, pool := newWagerDeps(t)
	walletID, playerID, _ := openWallet(t, create, "100.00")

	first, err := deps.ProcessWager(context.Background(), operation(wagering.Bet, walletID, playerID, "80.00", "bet-a"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := deps.ProcessWager(context.Background(), operation(wagering.Bet, walletID, playerID, "80.00", "bet-b"))
	if err != nil {
		t.Fatal(err)
	}
	processed, rejected := 0, 0
	for _, result := range []command.ProcessWagerResult{first, second} {
		switch result.Status {
		case wagering.Processed:
			processed++
		case wagering.Rejected:
			rejected++
			if result.FailureCode != wagering.InsufficientFunds {
				t.Fatalf("unexpected failure code: %v", result.FailureCode)
			}
		default:
			t.Fatalf("unexpected status: %v", result.Status)
		}
	}
	if processed != 1 || rejected != 1 {
		t.Fatalf("expected one processed and one rejected: %d/%d", processed, rejected)
	}
	if balance, _ := walletBalance(t, pool, walletID); balance != 2000 {
		t.Fatalf("final balance: %d, want 2000", balance)
	}
	debits := countRows(t, pool, "SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1 AND direction = 'DEBIT'", walletID)
	if debits != 1 {
		t.Fatalf("debits: %d, want exactly 1", debits)
	}
}

func TestProcessWagerConcurrentTwoBetsOf80Integration(t *testing.T) {
	deps, create, pool := newWagerDeps(t)
	walletID, playerID, _ := openWallet(t, create, "100.00")

	inputs := []command.ProcessWagerInput{
		operation(wagering.Bet, walletID, playerID, "80.00", "bet-c1"),
		operation(wagering.Bet, walletID, playerID, "80.00", "bet-c2"),
	}
	results := make([]command.ProcessWagerResult, len(inputs))
	errs := make([]error, len(inputs))
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range inputs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = deps.ProcessWager(context.Background(), inputs[i])
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent bet %d: %v", i, err)
		}
	}
	processed, rejected := 0, 0
	for _, result := range results {
		switch result.Status {
		case wagering.Processed:
			processed++
		case wagering.Rejected:
			rejected++
			if result.FailureCode != wagering.InsufficientFunds {
				t.Fatalf("unexpected rejection: %+v", result)
			}
		default:
			t.Fatalf("unexpected status %v", result.Status)
		}
	}
	if processed != 1 || rejected != 1 {
		t.Fatalf("concurrent dispute: processed %d, rejected %d", processed, rejected)
	}
	if balance, version := walletBalance(t, pool, walletID); balance != 2000 || version != 2 {
		t.Fatalf("final balance after concurrent bets: %d, want 2000", balance)
	}
	debits := countRows(t, pool, "SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1 AND direction = 'DEBIT'", walletID)
	if debits != 1 {
		t.Fatalf("debits after concurrent bets: %d, want exactly 1", debits)
	}
	assertReconciled(t, pool, walletID)
}

func TestProcessWagerRefundAndSingleCompensationIntegration(t *testing.T) {
	deps, create, pool := newWagerDeps(t)
	walletID, playerID, _ := openWallet(t, create, "100.00")
	bet := operation(wagering.Bet, walletID, playerID, "25.00", "bet-r")
	if _, err := deps.ProcessWager(context.Background(), bet); err != nil {
		t.Fatal(err)
	}

	refund := operation(wagering.Refund, walletID, playerID, "25.00", "refund-r")
	refund.ReferenceExternalTransactionID = bet.ExternalTransactionID
	refunded, err := deps.ProcessWager(context.Background(), refund)
	if err != nil {
		t.Fatal(err)
	}
	if refunded.Status != wagering.Processed {
		t.Fatalf("refund status: %v", refunded.Status)
	}
	if balance, version := walletBalance(t, pool, walletID); balance != 10000 || version != 3 {
		t.Fatalf("wallet after refund: (%d, %d)", balance, version)
	}

	secondRefund := operation(wagering.Refund, walletID, playerID, "25.00", "refund-r2")
	secondRefund.ReferenceExternalTransactionID = bet.ExternalTransactionID
	rejected, err := deps.ProcessWager(context.Background(), secondRefund)
	if err != nil {
		t.Fatal(err)
	}
	if rejected.Status != wagering.Rejected || rejected.FailureCode != wagering.AlreadyReversed {
		t.Fatalf("double compensation: %+v", rejected)
	}
	if balance, _ := walletBalance(t, pool, walletID); balance != 10000 {
		t.Fatalf("balance changed by rejected compensation: %d", balance)
	}
	credits := countRows(t, pool, "SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1 AND direction = 'CREDIT'", walletID)
	if credits != 2 { // opening + refund
		t.Fatalf("credits: %d, want 2", credits)
	}
}

func TestProcessWagerPendingReferenceIntegration(t *testing.T) {
	deps, create, pool := newWagerDeps(t)
	walletID, playerID, _ := openWallet(t, create, "100.00")

	refund := operation(wagering.Refund, walletID, playerID, "25.00", "refund-early")
	refund.ReferenceExternalTransactionID = "tx-bet-that-did-not-arrive"
	result, err := deps.ProcessWager(context.Background(), refund)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != wagering.PendingReference || result.HasBalance {
		t.Fatalf("unexpected result: %+v", result)
	}
	if balance, version := walletBalance(t, pool, walletID); balance != 10000 || version != 1 {
		t.Fatalf("pending reference must not move the wallet: (%d, %d)", balance, version)
	}
	pending := countRows(t, pool, `
		SELECT count(*) FROM wager_transactions
		WHERE external_transaction_id = $1 AND status = 'PENDING_REFERENCE'`, refund.ExternalTransactionID)
	if pending != 1 {
		t.Fatalf("pending rows: %d", pending)
	}
	events := countRows(t, pool, `
		SELECT count(*) FROM outbox_events WHERE event_type = 'WagerTransactionPendingReference'
		  AND payload_json->'data'->>'transactionId' = $1`, result.TransactionID.String())
	if events != 1 {
		t.Fatalf("pending reference events: %d", events)
	}
	// The pending transaction is recoverable through a fresh repository, and a
	// replay only reports the durable pending state (the worker comes later).
	deps.WagerLookups = postgres.NewWagerTransactionReader(pool)
	replay, err := deps.ProcessWager(t.Context(), refund)
	if err != nil || !replay.IdempotentReplay || replay.TransactionID != result.TransactionID || replay.Status != wagering.PendingReference {
		t.Fatalf("pending replay: %+v %v", replay, err)
	}
	if n := countRows(t, pool, "SELECT count(*) FROM wallet_ledger_entries WHERE transaction_id=$1", result.TransactionID); n != 0 {
		t.Fatal("pending operation wrote ledger")
	}
	assertReconciled(t, pool, walletID)

}

func TestProcessWagerWinWithReferenceAndLossIntegration(t *testing.T) {
	deps, create, pool := newWagerDeps(t)
	walletID, playerID, _ := openWallet(t, create, "100.00")
	bet := operation(wagering.Bet, walletID, playerID, "25.00", "bet-w")
	if _, err := deps.ProcessWager(context.Background(), bet); err != nil {
		t.Fatal(err)
	}

	win := operation(wagering.Win, walletID, playerID, "40.00", "win-w")
	win.ReferenceExternalTransactionID = bet.ExternalTransactionID
	won, err := deps.ProcessWager(context.Background(), win)
	if err != nil {
		t.Fatal(err)
	}
	if won.Status != wagering.Processed || won.Balance.MinorUnits() != 11500 {
		t.Fatalf("win with reference: %+v", won)
	}

	loss := operation(wagering.Loss, walletID, playerID, "0.00", "loss-w")
	lost, err := deps.ProcessWager(context.Background(), loss)
	if err != nil {
		t.Fatal(err)
	}
	if lost.Status != wagering.Processed || lost.WalletVersion != won.WalletVersion {
		t.Fatalf("loss: %+v", lost)
	}
	if balance, version := walletBalance(t, pool, walletID); balance != 11500 || version != won.WalletVersion {
		t.Fatalf("LOSS must not change the wallet: (%d, %d)", balance, version)
	}
	entries := countRows(t, pool, "SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1", walletID)
	if entries != 3 { // opening credit + bet debit + win credit
		t.Fatalf("ledger entries: %d, want 3", entries)
	}
}

func TestProcessWagerWalletNotFoundIntegration(t *testing.T) {
	deps, _, _ := newWagerDeps(t)
	input := operation(wagering.Bet, uuid.New(), uuid.New(), "25.00", "bet-ghost")
	if _, err := deps.ProcessWager(context.Background(), input); !errors.Is(err, ports.ErrWalletNotFound) {
		t.Fatalf("got %v, want ErrWalletNotFound", err)
	}
}

func assertReconciled(t *testing.T, pool *pgxpool.Pool, walletID uuid.UUID) {
	t.Helper()
	var stored, calculated int64
	err := pool.QueryRow(t.Context(), `SELECT w.balance_cents,
   COALESCE((SELECT sum(CASE WHEN direction='CREDIT' THEN amount_cents::numeric ELSE -amount_cents::numeric END)
   FROM wallet_ledger_entries WHERE wallet_id=w.id), 0)::bigint FROM wallets w WHERE id=$1`, walletID).Scan(&stored, &calculated)
	if err != nil || stored != calculated {
		t.Fatalf("reconciliation: stored=%d calculated=%d err=%v", stored, calculated, err)
	}
}

func TestProcessWagerFiftyConcurrentReplaysIntegration(t *testing.T) {
	deps, create, pool := newWagerDeps(t)
	walletID, playerID, _ := openWallet(t, create, "100.00")
	input := operation(wagering.Bet, walletID, playerID, "25.00", "fifty")
	const n = 50
	results := make([]command.ProcessWagerResult, n)
	errs := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	for i := range n {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; results[i], errs[i] = deps.ProcessWager(ctx, input) }()
	}
	close(start)
	wg.Wait()
	originals := 0
	for i, result := range results {
		if errs[i] != nil {
			t.Fatalf("request %d: %v", i, errs[i])
		}
		if result.Status != wagering.Processed || result.TransactionID != results[0].TransactionID || result.Balance.MinorUnits() != 7500 || result.WalletVersion != 2 {
			t.Fatalf("request %d: %+v", i, result)
		}
		if !result.IdempotentReplay {
			originals++
		}
	}
	if originals != 1 {
		t.Fatalf("original executions: %d", originals)
	}
	if n := countRows(t, pool, "SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id=$1 AND direction='DEBIT'", walletID); n != 1 {
		t.Fatalf("debits=%d", n)
	}
	if n := countRows(t, pool, "SELECT count(*) FROM wager_transactions WHERE wallet_id=$1 AND kind='BET'", walletID); n != 1 {
		t.Fatalf("bets=%d", n)
	}
	if n := countRows(t, pool, "SELECT count(*) FROM outbox_events WHERE payload_json->>'causationId'=$1", results[0].TransactionID.String()); n != 2 {
		t.Fatalf("events=%d", n)
	}
	assertReconciled(t, pool, walletID)
}

func TestProcessWagerReplayKeepsHistoricalResultIntegration(t *testing.T) {
	deps, create, pool := newWagerDeps(t)
	walletID, playerID, _ := openWallet(t, create, "100.00")
	bet := operation(wagering.Bet, walletID, playerID, "25.00", "history")
	first, err := deps.ProcessWager(t.Context(), bet)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := deps.ProcessWager(t.Context(), operation(wagering.Win, walletID, playerID, "50.00", "later")); err != nil {
		t.Fatal(err)
	}
	// A new adapter and UnitOfWork resolve the stored result, without cached state.
	deps.UnitOfWork = postgres.NewUnitOfWork(pool)
	deps.WagerLookups = postgres.NewWagerTransactionReader(pool)
	replay, err := deps.ProcessWager(t.Context(), bet)
	if err != nil {
		t.Fatal(err)
	}
	replay.IdempotentReplay = false
	if replay != first {
		t.Fatalf("historical replay=%+v original=%+v", replay, first)
	}
	if balance, version := walletBalance(t, pool, walletID); balance != 12500 || version != 3 {
		t.Fatalf("wallet: %d/%d", balance, version)
	}
	assertReconciled(t, pool, walletID)
}

func TestProcessWagerRollbackKindsIntegration(t *testing.T) {
	for _, kind := range []wagering.Kind{wagering.Bet, wagering.Win, wagering.Refund} {
		t.Run(string(kind), func(t *testing.T) {
			deps, create, pool := newWagerDeps(t)
			walletID, playerID, _ := openWallet(t, create, "100.00")
			original := operation(kind, walletID, playerID, "25.00", "original")
			if kind == wagering.Refund {
				bet := operation(wagering.Bet, walletID, playerID, "25.00", "prior-bet")
				if _, err := deps.ProcessWager(t.Context(), bet); err != nil {
					t.Fatal(err)
				}
				original.ReferenceExternalTransactionID = bet.ExternalTransactionID
			}
			if _, err := deps.ProcessWager(t.Context(), original); err != nil {
				t.Fatal(err)
			}
			reversal := operation(wagering.Rollback, walletID, playerID, "25.00", "rollback")
			reversal.ReferenceExternalTransactionID = original.ExternalTransactionID
			result, err := deps.ProcessWager(t.Context(), reversal)
			want := int64(10000)
			if kind == wagering.Refund {
				want = 7500
			}
			if err != nil || result.Status != wagering.Processed || result.Balance.MinorUnits() != want {
				t.Fatalf("rollback: %+v err=%v", result, err)
			}
			assertReconciled(t, pool, walletID)
		})
	}
}

func TestProcessWagerReversalInsufficientFundsIntegration(t *testing.T) {
	deps, create, pool := newWagerDeps(t)
	walletID, playerID, _ := openWallet(t, create, "0.00")
	win := operation(wagering.Win, walletID, playerID, "25.00", "win")
	if _, err := deps.ProcessWager(t.Context(), win); err != nil {
		t.Fatal(err)
	}
	if _, err := deps.ProcessWager(t.Context(), operation(wagering.Bet, walletID, playerID, "25.00", "spent")); err != nil {
		t.Fatal(err)
	}
	reversal := operation(wagering.Rollback, walletID, playerID, "25.00", "rollback")
	reversal.ReferenceExternalTransactionID = win.ExternalTransactionID
	result, err := deps.ProcessWager(t.Context(), reversal)
	if err != nil || result.Status != wagering.Rejected || result.FailureCode != wagering.ReversalInsufficientFunds {
		t.Fatalf("reversal: %+v err=%v", result, err)
	}
	if n := countRows(t, pool, "SELECT count(*) FROM wallet_ledger_entries WHERE transaction_id=$1", result.TransactionID); n != 0 {
		t.Fatal("rejected reversal wrote ledger")
	}
	replay, err := deps.ProcessWager(t.Context(), reversal)
	if err != nil || !replay.IdempotentReplay || replay.FailureCode != result.FailureCode {
		t.Fatalf("rejected replay: %+v err=%v", replay, err)
	}
	assertReconciled(t, pool, walletID)
}

func TestProcessWagerIncompatibleReferencesIntegration(t *testing.T) {
	for _, name := range []string{"round", "wallet", "amount", "kind", "rejected", "provider"} {
		t.Run(name, func(t *testing.T) {
			deps, create, pool := newWagerDeps(t)
			walletID, playerID, _ := openWallet(t, create, "100.00")
			original := operation(wagering.Bet, walletID, playerID, "25.00", "reference")
			refund := operation(wagering.Refund, walletID, playerID, "25.00", "refund")
			want := wagering.ReferenceScopeMismatch
			switch name {
			case "round":
				refund.RoundID = "other"
			case "wallet":
				refund.WalletID, refund.PlayerID, _ = openWallet(t, create, "100.00")
			case "amount":
				refund.Money = parseBRL(t, "20.00")
				want = wagering.ReferenceAmountMismatch
			case "kind":
				original.Kind = wagering.Win
				want = wagering.InvalidReferenceKind
			case "rejected":
				original.Money = parseBRL(t, "200.00")
				refund.Money = original.Money
				want = wagering.ReferenceNotProcessed
			case "provider":
				refund.ProviderID = "provider-b"
			}
			if _, err := deps.ProcessWager(t.Context(), original); err != nil {
				t.Fatal(err)
			}
			refund.ReferenceExternalTransactionID = original.ExternalTransactionID
			result, err := deps.ProcessWager(t.Context(), refund)
			if err != nil {
				t.Fatal(err)
			}
			if name == "provider" {
				if result.Status != wagering.PendingReference {
					t.Fatalf("other provider reference exposed: %+v", result)
				}
			} else if result.Status != wagering.Rejected || result.FailureCode != want {
				t.Fatalf("reference result: %+v want=%s", result, want)
			}
			if n := countRows(t, pool, "SELECT count(*) FROM wallet_ledger_entries WHERE transaction_id=$1", result.TransactionID); n != 0 {
				t.Fatal("invalid reference wrote ledger")
			}
			assertReconciled(t, pool, walletID)
			assertReconciled(t, pool, refund.WalletID)
		})
	}
}

// Fail on the second outbox insert using a real unique violation, after the
// wallet, transaction, ledger and first event were written in the real SQL tx.
type duplicateEventOutbox struct {
	repository ports.OutboxRepository
	first      *events.Envelope
}

func (f *duplicateEventOutbox) InsertEvent(ctx context.Context, e events.Envelope) error {
	if f.first == nil {
		f.first = &e
		return f.repository.InsertEvent(ctx, e)
	}
	return f.repository.InsertEvent(ctx, *f.first)
}

func TestProcessWagerSQLFailureRollsBackEverythingIntegration(t *testing.T) {
	deps, create, pool := newWagerDeps(t)
	walletID, playerID, _ := openWallet(t, create, "100.00")
	input := operation(wagering.Bet, walletID, playerID, "25.00", "sql-failure")
	deps.Outbox = &duplicateEventOutbox{repository: postgres.NewOutboxRepository()}
	_, err := deps.ProcessWager(t.Context(), input)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("expected SQL unique violation: %v", err)
	}
	if balance, version := walletBalance(t, pool, walletID); balance != 10000 || version != 1 {
		t.Fatalf("wallet escaped rollback: %d/%d", balance, version)
	}
	if n := countRows(t, pool, "SELECT count(*) FROM wager_transactions WHERE wallet_id=$1", walletID); n != 1 {
		t.Fatalf("transactions=%d", n)
	}
	if n := countRows(t, pool, "SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id=$1", walletID); n != 1 {
		t.Fatalf("ledger=%d", n)
	}
	if n := countRows(t, pool, "SELECT count(*) FROM outbox_events WHERE payload_json->'data'->>'walletId'=$1", walletID.String()); n != 2 {
		t.Fatalf("outbox=%d", n)
	}
	deps.Outbox = postgres.NewOutboxRepository()
	result, err := deps.ProcessWager(t.Context(), input)
	if err != nil || result.Status != wagering.Processed || result.IdempotentReplay {
		t.Fatalf("retry after rollback: %+v %v", result, err)
	}
	assertReconciled(t, pool, walletID)
}

func TestProcessWagerIndependentWalletProgressesWhileAnotherIsLockedIntegration(t *testing.T) {
	deps, create, pool := newWagerDeps(t)
	first, firstPlayer, _ := openWallet(t, create, "100.00")
	second, secondPlayer, _ := openWallet(t, create, "100.00")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, "SELECT id FROM wallets WHERE id=$1 FOR UPDATE", first); err != nil {
		t.Fatal(err)
	}
	blocked := make(chan error, 1)
	go func() {
		_, err := deps.ProcessWager(ctx, operation(wagering.Bet, first, firstPlayer, "25.00", "blocked"))
		blocked <- err
	}()
	// Wait for PostgreSQL to report the waiter, instead of assuming scheduling.
	for {
		var waiting bool
		err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%FOR UPDATE%')`).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(5 * time.Millisecond):
		}
	}
	fastCtx, fastCancel := context.WithTimeout(ctx, 2*time.Second)
	defer fastCancel()
	result, err := deps.ProcessWager(fastCtx, operation(wagering.Bet, second, secondPlayer, "25.00", "independent"))
	if err != nil || result.Status != wagering.Processed {
		t.Fatalf("independent wallet blocked: %+v %v", result, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-blocked; err != nil {
		t.Fatal(err)
	}
	assertReconciled(t, pool, first)
	assertReconciled(t, pool, second)
}

func TestProcessWagerConcurrentCompensationsIntegration(t *testing.T) {
	deps, create, pool := newWagerDeps(t)
	walletID, playerID, _ := openWallet(t, create, "100.00")
	bet := operation(wagering.Bet, walletID, playerID, "25.00", "bet")
	if _, err := deps.ProcessWager(t.Context(), bet); err != nil {
		t.Fatal(err)
	}
	inputs := []command.ProcessWagerInput{
		operation(wagering.Refund, walletID, playerID, "25.00", "refund"),
		operation(wagering.Rollback, walletID, playerID, "25.00", "rollback"),
	}
	results := make([]command.ProcessWagerResult, 2)
	errs := make([]error, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range inputs {
		inputs[i].ReferenceExternalTransactionID = bet.ExternalTransactionID
		wg.Add(1)
		go func() { defer wg.Done(); <-start; results[i], errs[i] = deps.ProcessWager(t.Context(), inputs[i]) }()
	}
	close(start)
	wg.Wait()
	processed, rejected := 0, 0
	for i, result := range results {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		switch result.Status {
		case wagering.Processed:
			processed++
		case wagering.Rejected:
			rejected++
			if result.FailureCode != wagering.AlreadyReversed {
				t.Fatalf("rejection: %+v", result)
			}
		}
	}
	if processed != 1 || rejected != 1 {
		t.Fatalf("processed=%d rejected=%d", processed, rejected)
	}
	if balance, version := walletBalance(t, pool, walletID); balance != 10000 || version != 3 {
		t.Fatalf("wallet=%d/%d", balance, version)
	}
	assertReconciled(t, pool, walletID)
}

// Hold each request after its second key lookup (already under its own wallet
// lock). Both observe absence before racing on the database's unique index.
type synchronizedLookups struct {
	ports.WagerTransactionReader
	calls   int
	ready   chan<- struct{}
	release <-chan struct{}
}

func (r *synchronizedLookups) FindByIdempotencyKey(ctx context.Context, provider, key string) (wagering.State, error) {
	state, err := r.WagerTransactionReader.FindByIdempotencyKey(ctx, provider, key)
	r.calls++
	if r.calls == 2 {
		r.ready <- struct{}{}
		select {
		case <-r.release:
		case <-ctx.Done():
			return wagering.State{}, ctx.Err()
		}
	}
	return state, err
}

func TestProcessWagerConflictingIdentityAcrossWalletsIntegration(t *testing.T) {
	deps, create, pool := newWagerDeps(t)
	first, p1, _ := openWallet(t, create, "100.00")
	second, p2, _ := openWallet(t, create, "100.00")
	inputs := []command.ProcessWagerInput{
		operation(wagering.Bet, first, p1, "25.00", "identity-race"),
		operation(wagering.Bet, second, p2, "25.00", "identity-race"),
	}
	inputs[1].ExternalTransactionID = inputs[0].ExternalTransactionID
	inputs[1].IdempotencyKey = inputs[0].IdempotencyKey
	ready, release := make(chan struct{}, 2), make(chan struct{})
	results := make([]command.ProcessWagerResult, 2)
	errs := make([]error, 2)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i := range inputs {
		local := deps
		local.WagerLookups = &synchronizedLookups{WagerTransactionReader: postgres.NewWagerTransactionReader(pool), ready: ready, release: release}
		wg.Add(1)
		go func() { defer wg.Done(); results[i], errs[i] = local.ProcessWager(ctx, inputs[i]) }()
	}
	for range 2 {
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	close(release)
	wg.Wait()
	successes, conflicts := 0, 0
	for i, err := range errs {
		if errors.Is(err, ports.ErrIdempotencyConflict) {
			conflicts++
			continue
		}
		if err != nil || results[i].Status != wagering.Processed {
			t.Fatalf("operation %d: %+v %v", i, results[i], err)
		}
		successes++
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
	if n := countRows(t, pool, "SELECT count(*) FROM wager_transactions WHERE provider_id=$1 AND external_transaction_id=$2", inputs[0].ProviderID, inputs[0].ExternalTransactionID); n != 1 {
		t.Fatalf("transactions=%d", n)
	}
	assertReconciled(t, pool, first)
	assertReconciled(t, pool, second)
}
