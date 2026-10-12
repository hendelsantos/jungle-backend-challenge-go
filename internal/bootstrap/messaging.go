package bootstrap

import (
	"context"
	"log/slog"
	"time"

	"go.uber.org/fx"

	"github.com/hendelsantos/jungle-backend-challenge-go/internal/adapters/postgres"
	sqsadapter "github.com/hendelsantos/jungle-backend-challenge-go/internal/adapters/sqs"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/command"
)

// messagingModule is composed when SQS_ENDPOINT_URL and DATABASE_URL are set.
// The consumer shares the pool, the UnitOfWork and the ProcessWager use case
// with the HTTP boundary; the inbox completion commits with the wager.
func messagingModule() fx.Option {
	return fx.Module("messaging",
		fx.Provide(newSQSClient, newConsumeWagerDeps, newSQSConsumer),
		fx.Invoke(registerSQSConsumer),
	)
}

func newSQSClient(cfg Config) (*sqsadapter.ManagedClient, error) {
	// Provision is idempotent and retries while the broker warms up; failing
	// fast here mirrors the database ping: no queues, no serving.
	return sqsadapter.NewManagedClient(context.Background(), sqsadapter.Config{
		EndpointURL: cfg.SQSEndpointURL, Region: cfg.SQSRegion, QueuePrefix: cfg.SQSQueuePrefix,
	})
}

func newConsumeWagerDeps(
	uow *postgres.UnitOfWork,
	process command.ProcessWagerDeps,
	inbox *postgres.InboxRepository,
) command.ConsumeWagerDeps {
	return command.ConsumeWagerDeps{
		UnitOfWork: uow,
		Process:    process,
		Inbox:      inbox,
		Now:        func() time.Time { return time.Now().UTC() },
	}
}

func newSQSConsumer(cfg Config, managed *sqsadapter.ManagedClient, consume command.ConsumeWagerDeps, logger *slog.Logger) *sqsadapter.Consumer {
	return &sqsadapter.Consumer{
		Consume:       consume.ConsumeWagerMessage,
		Client:        managed.Client,
		WagerQueueURL: managed.Queues.WagerQueueURL,
		DLQQueueURL:   managed.Queues.DLQQueueURL,
		ConsumerName:  "wager-consumer",
		Logger:        logger,
		ProviderKeys:  cfg.SQSProviderKeys,
	}
}

// registerSQSConsumer starts polling after the queues are provisioned and
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
