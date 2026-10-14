package bootstrap

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	httpapi "github.com/hendelsantos/jungle-backend-challenge-go/internal/adapters/http"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/adapters/postgres"
	sqsadapter "github.com/hendelsantos/jungle-backend-challenge-go/internal/adapters/sqs"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/command"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/observability"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/workers"
	"go.uber.org/fx"
)

type OutboxWorkerCount int

func newOutboxWorkerCount(cfg Config) OutboxWorkerCount {
	if cfg.OutboxWorkers <= 0 {
		return 1
	}
	return OutboxWorkerCount(cfg.OutboxWorkers)
}

func newOutboxMetrics() *observability.OutboxMetrics { return observability.NewOutboxMetrics() }

type PublishAckPause time.Duration

func newPublishAckPause(cfg Config) PublishAckPause { return PublishAckPause(cfg.TestPublishAckPause) }

func newOutboxWorker(store *postgres.OutboxDeliveryRepository, managed *sqsadapter.ManagedClient, metrics *observability.OutboxMetrics, logger *slog.Logger, publishAckPause PublishAckPause) *workers.OutboxWorker {
	deps := command.PublishOutboxDeps{Outbox: store, Publisher: &sqsadapter.EventPublisher{Client: managed.Client, QueueURL: managed.Queues.EventsQueueURL}, Metrics: metrics, Logger: logger, PostSendPause: time.Duration(publishAckPause)}
	return &workers.OutboxWorker{PublishNext: deps.PublishNext, Interval: time.Second, Logger: logger}
}
func registerOutboxMetrics(server *http.Server, store *postgres.OutboxDeliveryRepository, pool *pgxpool.Pool) {
	httpapi.RegisterMetrics(server.Handler.(*http.ServeMux), store, func() httpapi.PoolGauges {
		stat := pool.Stat()
		return httpapi.PoolGauges{
			Total:             int(stat.TotalConns()),
			Idle:              int(stat.IdleConns()),
			Acquired:          int(stat.AcquiredConns()),
			AcquireDurationMs: stat.AcquireDuration().Seconds() * 1000 / float64(max(stat.AcquiredConns(), 1)),
		}
	})
}

// registerOutboxWorker starts the configured number of independent publish
// loops. Each worker claims rows with FOR UPDATE SKIP LOCKED, so concurrency
// is bounded by exclusive leases and drain rate grows with worker count.
func registerOutboxWorker(lifecycle fx.Lifecycle, worker *workers.OutboxWorker, count OutboxWorkerCount, logger *slog.Logger) {
	var cancel context.CancelFunc
	var wg sync.WaitGroup
	var finished = make(chan struct{})
	lifecycle.Append(fx.Hook{
		OnStart: func(context.Context) error {
			var ctx context.Context
			ctx, cancel = context.WithCancel(context.Background())
			wg.Add(int(count))
			for i := 0; i < int(count); i++ {
				go func() {
					defer wg.Done()
					worker.Run(ctx)
				}()
			}
			go func() {
				wg.Wait()
				close(finished)
			}()
			logger.Info("outbox publisher started", "workers", int(count))
			return nil
		},
		OnStop: func(ctx context.Context) error {
			cancel()
			select {
			case <-finished:
				logger.Info("outbox publisher stopped", "workers", int(count))
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	})
}
