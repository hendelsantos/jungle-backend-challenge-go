//go:build integration

package main

import (
	"context"
	"github.com/google/uuid"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/adapters/postgres"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/command"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/money"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wagering"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/testsupport"
	"github.com/jackc/pgx/v5"
	"strings"
	"testing"
	"time"
)

type auditTraceKey struct{}
type auditWriter struct {
	write func()
	fired bool
}

func (w *auditWriter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, auditTraceKey{}, strings.Contains(data.SQL, "SELECT balance_cents FROM wallets"))
}
func (w *auditWriter) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	if match, _ := ctx.Value(auditTraceKey{}).(bool); match && !w.fired {
		w.fired = true
		w.write()
	}
}
func TestAuditUsesOneSnapshotDuringConcurrentCommitIntegration(t *testing.T) {
	pool := testsupport.IsolatedPostgresPool(t)
	uow := postgres.NewUnitOfWork(pool)
	wallets := postgres.NewWalletRepository()
	wagers := postgres.NewWagerTransactionRepository()
	ledger := postgres.NewLedgerRepository()
	outbox := postgres.NewOutboxRepository()
	create := command.CreateWalletDeps{UnitOfWork: uow, Wallets: wallets, Wagers: wagers, Ledger: ledger, Outbox: outbox, Now: time.Now, NewID: uuid.New}
	amount, _ := money.ParseDecimal("100.00", money.BRL)
	player := uuid.New()
	opened, err := create.CreateWallet(t.Context(), command.CreateWalletInput{PlayerID: player, InitialBalance: amount, CorrelationID: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	process := command.ProcessWagerDeps{UnitOfWork: uow, Wallets: wallets, Wagers: wagers, WagerLookups: postgres.NewWagerTransactionReader(pool), Ledger: ledger, Outbox: outbox, Now: time.Now, NewID: uuid.New}
	tracer := &auditWriter{write: func() {
		win, _ := money.ParseDecimal("25.00", money.BRL)
		_, err := process.ProcessWager(t.Context(), command.ProcessWagerInput{ProviderID: "provider-a", ExternalTransactionID: "snapshot-win", IdempotencyKey: "snapshot-win", PlayerID: player, WalletID: opened.WalletID, RoundID: "round", GameID: "game", Kind: wagering.Win, Money: win, CorrelationID: uuid.New()})
		if err != nil {
			t.Fatal(err)
		}
	}}
	cfg := pool.Config().ConnConfig.Copy()
	cfg.Tracer = tracer
	conn, err := pgx.ConnectConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	want := int64(10000)
	_, issues := audit(t.Context(), conn, opened.WalletID.String(), &want, 1, true, map[string]int64{"OPENING": 1}, nil, 0, 2)
	if !tracer.fired || len(issues) > 0 {
		t.Fatalf("concurrent snapshot: fired=%v issues=%v", tracer.fired, issues)
	}
	var current int64
	if err := pool.QueryRow(t.Context(), "SELECT balance_cents FROM wallets WHERE id=$1", opened.WalletID).Scan(&current); err != nil || current != 12500 {
		t.Fatalf("writer did not commit: balance=%d err=%v", current, err)
	}
}
