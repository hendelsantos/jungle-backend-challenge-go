package bootstrap

import (
	"context"
	"log/slog"
	"time"

	"go.uber.org/fx"

	"github.com/hendelsantos/jungle-backend-challenge-go/internal/adapters/postgres"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/command"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wagering"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/observability"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/workers"
)

// workersModule is composed with the database and application modules: the
// reference worker owns no connections, it shares the pool and the use cases.
func workersModule() fx.Option {
	return fx.Module("workers",
		fx.Provide(newRetryReferenceDeps, newReferenceWorker),
		fx.Invoke(registerReferenceWorker),
	)
}

func newRetryReferenceDeps(
	cfg Config,
	uow *postgres.UnitOfWork,
	wallets *postgres.WalletRepository,
	wagers *postgres.WagerTransactionRepository,
	lookups *postgres.WagerTransactionReader,
	ledgerRepository *postgres.LedgerRepository,
	outbox *postgres.OutboxRepository,
	finance *observability.FinancialMetrics,
) command.RetryReferenceDeps {
	return command.RetryReferenceDeps{
		UnitOfWork:   uow,
		Wallets:      wallets,
		Wagers:       wagers,
		Lookups:      lookups,
		Pending:      lookups,
		Ledger:       ledgerRepository,
		Outbox:       outbox,
		Now:          func() time.Time { return time.Now().UTC() },
		NewID:        newUUID,
		ReferenceTTL: cfg.ReferenceTTL,
		OnConclusion: func(kind wagering.Kind, status wagering.Status) {
			finance.Operations.WithLabelValues(string(kind), string(status)).Inc()
		},
		OnRetry: func() { finance.DbRetries.WithLabelValues("reference_transaction").Inc() },
	}
}

func newReferenceWorker(cfg Config, retry command.RetryReferenceDeps, logger *slog.Logger) *workers.ReferenceWorker {
	return &workers.ReferenceWorker{
		Retry:      retry.RetryDueReferences,
		Interval:   cfg.ReferenceWorkerInterval,
		BatchLimit: 50,
		Logger:     logger,
	}
}

// registerReferenceWorker starts the polling loop after every dependency is
// ready and stops it before the pool closes (Fx runs OnStop hooks in reverse
// registration order; the database hook is registered first). Cancellation
// stops new polls; an in-flight batch either commits or rolls back, leaving
// claimed pending references due again for any instance.
func registerReferenceWorker(lifecycle fx.Lifecycle, worker *workers.ReferenceWorker, logger *slog.Logger) {
	var (
		cancel context.CancelFunc
		done   chan struct{}
	)
	lifecycle.Append(fx.Hook{
		OnStart: func(context.Context) error {
			var runCtx context.Context
			runCtx, cancel = context.WithCancel(context.Background())
			done = make(chan struct{})
			go func() {
				defer close(done)
				worker.Run(runCtx)
			}()
			logger.Info("reference worker started", "interval", worker.Interval.String())
			return nil
		},
		OnStop: func(ctx context.Context) error {
			cancel()
			select {
			case <-done:
				logger.Info("reference worker stopped")
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	})
}
