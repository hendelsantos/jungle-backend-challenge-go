package bootstrap

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	httpapi "github.com/hendelsantos/jungle-backend-challenge-go/internal/adapters/http"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/adapters/postgres"
	sqsadapter "github.com/hendelsantos/jungle-backend-challenge-go/internal/adapters/sqs"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/command"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/observability"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/workers"
	"go.uber.org/fx"
)

func newOutboxMetrics() *observability.OutboxMetrics { return observability.NewOutboxMetrics() }
func newOutboxWorker(store *postgres.OutboxDeliveryRepository, managed *sqsadapter.ManagedClient, metrics *observability.OutboxMetrics, logger *slog.Logger) *workers.OutboxWorker {
	deps := command.PublishOutboxDeps{Outbox: store, Publisher: &sqsadapter.EventPublisher{Client: managed.Client, QueueURL: managed.Queues.EventsQueueURL}, Metrics: metrics, Logger: logger}
	return &workers.OutboxWorker{PublishNext: deps.PublishNext, Interval: time.Second, Logger: logger}
}
func registerOutboxMetrics(server *http.Server, store *postgres.OutboxDeliveryRepository) {
	httpapi.RegisterMetrics(server.Handler.(*http.ServeMux), store)
}
func registerOutboxWorker(lifecycle fx.Lifecycle, worker *workers.OutboxWorker, logger *slog.Logger) {
	var cancel context.CancelFunc
	var done chan struct{}
	lifecycle.Append(fx.Hook{
		OnStart: func(context.Context) error {
			var ctx context.Context
			ctx, cancel = context.WithCancel(context.Background())
			done = make(chan struct{})
			go func() { defer close(done); worker.Run(ctx) }()
			logger.Info("outbox publisher started")
			return nil
		},
		OnStop: func(ctx context.Context) error {
			cancel()
			select {
			case <-done:
				logger.Info("outbox publisher stopped")
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	})
}
