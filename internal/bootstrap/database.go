package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	httpapi "github.com/hendelsantos/jungle-backend-challenge-go/internal/adapters/http"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/adapters/postgres"
	sqsadapter "github.com/hendelsantos/jungle-backend-challenge-go/internal/adapters/sqs"
)

// databaseModule is composed only when DATABASE_URL is configured; until the
// business endpoints exist, the application may run without a database.
func databaseModule() fx.Option {
	return fx.Module("database",
		fx.Provide(newPostgresConfig, postgres.NewPool, postgres.NewUnitOfWork),
		fx.Invoke(registerDatabase, registerReadiness),
	)
}

// registerReadiness publishes the public readiness route when the database is
// composed; the SQS check joins the same route with the messaging module.
type readinessDeps struct {
	fx.In
	Server *http.Server
	Pool   *pgxpool.Pool
	SQS    *sqsadapter.ManagedClient `optional:"true"`
}

func registerReadiness(d readinessDeps) {
	checks := []httpapi.ReadinessCheck{{Name: "postgres", Check: d.Pool.Ping}}
	if d.SQS != nil {
		checks = append(checks, httpapi.ReadinessCheck{Name: "sqs", Check: d.SQS.Ready})
	}
	httpapi.RegisterReadiness(d.Server.Handler.(*http.ServeMux), checks...)
}

// newPostgresConfig keeps the dependency direction: bootstrap knows the
// adapter config type, the adapter never imports bootstrap.
func newPostgresConfig(cfg Config) postgres.Config {
	return postgres.Config{URL: cfg.DatabaseURL}
}

func registerDatabase(lifecycle fx.Lifecycle, pool *pgxpool.Pool, uow *postgres.UnitOfWork, logger *slog.Logger) {
	lifecycle.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			if err := pool.Ping(ctx); err != nil {
				return fmt.Errorf("ping database: %w", err)
			}
			logger.Info("database ready", "max_connections", pool.Config().MaxConns)
			return nil
		},
		OnStop: func(context.Context) error {
			// Closing is graceful and idempotent; in-flight transactions finish first.
			pool.Close()
			logger.Info("database stopped")
			return nil
		},
	})
}
