// provision-sqs is the administrative entry point. The server only resolves queues.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	sqsadapter "github.com/hendelsantos/jungle-backend-challenge-go/internal/adapters/sqs"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("SQS FIFO queues provisioned and configured")
}

func run(ctx context.Context) error {
	cfg := sqsadapter.Config{
		EndpointURL: os.Getenv("SQS_ENDPOINT_URL"), Region: os.Getenv("SQS_REGION"),
		QueuePrefix: os.Getenv("SQS_QUEUE_PREFIX"), Profile: os.Getenv("SQS_PROVISIONER_PROFILE"),
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	client, err := sqsadapter.NewClient(ctx, cfg)
	if err != nil {
		return err
	}
	if _, err := sqsadapter.ProvisionNamed(ctx, client, cfg.QueueNames()); err != nil {
		return fmt.Errorf("provision-sqs: administrative provisioning failed (check broker access and provisioner policy): %w", err)
	}
	return nil
}
