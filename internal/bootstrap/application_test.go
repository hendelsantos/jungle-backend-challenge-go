package bootstrap

import (
	"testing"

	"github.com/hendelsantos/jungle-backend-challenge-go/internal/adapters/postgres"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/command"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/observability"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"
)

func TestApplicationModuleResolvesProcessWager(t *testing.T) {
	app := fx.New(fx.NopLogger,
		fx.Supply(postgres.Config{URL: "postgres://localhost/jungle?sslmode=disable"}),
		fx.Provide(postgres.NewPool, postgres.NewUnitOfWork), applicationModule(),
		fx.Provide(observability.NewFinancialMetrics),
		fx.Invoke(func(deps command.ProcessWagerDeps, pool *pgxpool.Pool) {
			t.Cleanup(pool.Close)
			if deps.UnitOfWork == nil || deps.Wallets == nil || deps.Wagers == nil || deps.WagerLookups == nil || deps.Ledger == nil || deps.Outbox == nil || deps.Now == nil || deps.NewID == nil || deps.Financials == nil {
				t.Fatal("incomplete ProcessWager dependencies")
			}
		}),
	)
	if err := app.Err(); err != nil {
		t.Fatal(err)
	}
	// The graph uses a lazy pool and performs no database I/O.
}
