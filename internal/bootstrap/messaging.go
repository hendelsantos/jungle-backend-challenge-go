package bootstrap

import (
	"context"
	"log/slog"
	"time"

	"go.uber.org/fx"

	"github.com/hendelsantos/jungle-backend-challenge-go/internal/adapters/postgres"
	sqsadapter "github.com/hendelsantos/jungle-backend-challenge-go/internal/adapters/sqs"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/command"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/observability"
)

// messagingModule is composed when the SQS gate (SQS_ENABLED, or the
// historical SQS_ENDPOINT_URL behavior) and DATABASE_URL are set. The consumer
// shares the pool, the UnitOfWork and the ProcessWager use case with the HTTP
// boundary; the inbox completion commits with the wager.
func messagingModule() fx.Option {
	return fx.Module("messaging",
		fx.Provide(newSQSClient, newConsumeWagerDeps, newSQSConsumer, postgres.NewOutboxDeliveryRepository, newOutboxMetrics, newOutboxWorkerCount, newPublishAckPause, newOutboxWorker),
		fx.Invoke(registerSQSConsumer, registerOutboxWorker, registerOutboxMetrics),
	)
}

func newSQSClient(cfg Config) (*sqsadapter.ManagedClient, error) {
	consumer := sqsadapter.Config{
		EndpointURL: cfg.SQSEndpointURL, Region: cfg.SQSRegion, QueuePrefix: cfg.SQSQueuePrefix,
		Profile: cfg.SQSConsumerProfile,
	}
	publisher := consumer
	publisher.Profile = cfg.SQSPublisherProfile
	return sqsadapter.NewManagedClient(context.Background(), consumer, publisher)
}

func newConsumeWagerDeps(
	uow *postgres.UnitOfWork,
	process command.ProcessWagerDeps,
	inbox *postgres.InboxRepository,
	finance *observability.FinancialMetrics,
) command.ConsumeWagerDeps {
	return command.ConsumeWagerDeps{
		UnitOfWork: uow,
		Process:    process,
		Inbox:      inbox,
		OnRetry:    func() { finance.DbRetries.WithLabelValues("inbox_transaction").Inc() },
		Now:        func() time.Time { return time.Now().UTC() },
	}
}

func newSQSConsumer(cfg Config, managed *sqsadapter.ManagedClient, consume command.ConsumeWagerDeps, finance *observability.FinancialMetrics, logger *slog.Logger) *sqsadapter.Consumer {
	return &sqsadapter.Consumer{
		Consume:         consume.ConsumeWagerMessage,
		Client:          managed.Client,
		WagerQueueURL:   managed.Queues.WagerQueueURL,
		DLQQueueURL:     managed.Queues.DLQQueueURL,
		ConsumerName:    "wager-consumer",
		Metrics:         finance,
		PostCommitPause: cfg.TestPostCommitPause,
		Logger:          logger,
		ProviderKeys:    cfg.SQSProviderKeys,
	}
}

// registerSQSConsumer starts polling after the existing queues are validated and
// stops it before the pool closes. Cancellation aborts the in-flight receive;
// uncommitted work rolls back and the message redelivers safely.
func registerSQSConsumer(lifecycle fx.Lifecycle, consumer *sqsadapter.Consumer, logger *slog.Logger) {
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
				consumer.Run(runCtx)
			}()
			logger.Info("SQS consumer started")
			return nil
		},
		OnStop: func(ctx context.Context) error {
			cancel()
			select {
			case <-done:
				logger.Info("SQS consumer stopped")
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	})
}
