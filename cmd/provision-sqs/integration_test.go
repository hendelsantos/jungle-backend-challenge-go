//go:build integration

package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/google/uuid"
	sqsadapter "github.com/hendelsantos/jungle-backend-challenge-go/internal/adapters/sqs"
)

func TestProvisionAndResolveRealBroker(t *testing.T) {
	endpoint := os.Getenv("TEST_SQS_ENDPOINT")
	if endpoint == "" {
		t.Skip("TEST_SQS_ENDPOINT required")
	}
	prefix := "provision-" + uuid.NewString()[:8] + "-"
	t.Setenv("SQS_ENDPOINT_URL", endpoint)
	t.Setenv("SQS_REGION", "us-east-1")
	t.Setenv("SQS_QUEUE_PREFIX", prefix)
	t.Setenv("SQS_PROVISIONER_PROFILE", "")
	cfg := sqsadapter.Config{EndpointURL: endpoint, Region: "us-east-1", QueuePrefix: prefix}
	admin, err := sqsadapter.NewClient(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sqsadapter.NewManagedClient(t.Context(), cfg, cfg); err == nil {
		t.Fatal("runtime accepted absent queues")
	}
	if err := run(t.Context()); err != nil {
		t.Fatal(err)
	}
	runtime, err := sqsadapter.NewManagedClient(t.Context(), cfg, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, u := range []string{runtime.Queues.WagerQueueURL, runtime.Queues.DLQQueueURL, runtime.Queues.EventsQueueURL} {
			admin.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: aws.String(u)})
		}
	})
	if err := run(t.Context()); err != nil {
		t.Fatal(err)
	}
	again, err := sqsadapter.NewManagedClient(t.Context(), cfg, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if again.Queues != runtime.Queues {
		t.Fatal("second provision changed URLs")
	}
	if _, err := admin.SetQueueAttributes(t.Context(), &sqs.SetQueueAttributesInput{QueueUrl: aws.String(runtime.Queues.WagerQueueURL), Attributes: map[string]string{"VisibilityTimeout": "1"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := sqsadapter.NewManagedClient(t.Context(), cfg, cfg); err == nil {
		t.Fatal("runtime accepted incompatible attributes")
	}
	if err := run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Ready(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.DeleteQueue(t.Context(), &sqs.DeleteQueueInput{QueueUrl: aws.String(runtime.Queues.EventsQueueURL)}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Ready(t.Context()); err == nil {
		t.Fatal("readiness accepted a missing queue")
	}
}
