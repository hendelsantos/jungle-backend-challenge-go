package bootstrap

import (
	"testing"
	"time"

	"github.com/hendelsantos/jungle-backend-challenge-go/internal/adapters/postgres"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/command"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/workers"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"
)

func TestWorkersModuleResolvesRetryAndWorker(t *testing.T) {
	app := fx.New(fx.NopLogger,
		fx.Supply(
			postgres.Config{URL: "postgres://localhost/jungle?sslmode=disable"},
			Config{ReferenceTTL: time.Hour, ReferenceWorkerInterval: 250 * time.Millisecond},
		),
		fx.Provide(
			newLogger,
			postgres.NewPool, postgres.NewUnitOfWork,
			postgres.NewWalletRepository, postgres.NewWagerTransactionRepository,
			postgres.NewWagerTransactionReader, postgres.NewLedgerRepository,
			postgres.NewOutboxRepository,
		),
		workersModule(),
		fx.Invoke(func(retry command.RetryReferenceDeps, worker *workers.ReferenceWorker, pool *pgxpool.Pool) {
			t.Cleanup(pool.Close)
			if retry.UnitOfWork == nil || retry.Wallets == nil || retry.Wagers == nil ||
				retry.Lookups == nil || retry.Pending == nil || retry.Ledger == nil ||
				retry.Outbox == nil || retry.Now == nil || retry.NewID == nil {
				t.Fatal("incomplete RetryReference dependencies")
			}
			if retry.ReferenceTTL != time.Hour {
				t.Fatalf("ttl = %v", retry.ReferenceTTL)
			}
			if worker.Retry == nil || worker.Interval != 250*time.Millisecond || worker.BatchLimit < 1 {
				t.Fatalf("worker = %+v", worker)
			}
		}),
	)
	if err := app.Err(); err != nil {
		t.Fatal(err)
	}
	// The graph uses a lazy pool and performs no database I/O.
}
