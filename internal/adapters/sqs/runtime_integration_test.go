//go:build integration

package sqs_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"
	sqsadapter "github.com/hendelsantos/jungle-backend-challenge-go/internal/adapters/sqs"
)

// Uses real SQS to prove provisioning boundaries, not IAM enforcement.
func TestSQSRuntimeRequiresAdministrativeProvisioningIntegration(t *testing.T) {
	endpoint := os.Getenv("TEST_SQS_ENDPOINT")
	if endpoint == "" {
		t.Skip("TEST_SQS_ENDPOINT is not set: real SQS required")
	}
	region := os.Getenv("TEST_SQS_REGION")
	if region == "" {
		region = "us-east-1"
	}
	cfg := sqsadapter.Config{EndpointURL: endpoint, Region: region, QueuePrefix: "runtime-" + uuid.NewString()[:8] + "-"}
	admin, err := sqsadapter.NewClient(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	names := cfg.QueueNames()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		for _, name := range []string{names.Wager, names.Events, names.DLQ} {
			q, err := admin.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: aws.String(name)})
			if err == nil {
				if _, err := admin.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: q.QueueUrl}); err != nil {
					t.Error(err)
				}
			}
		}
	})
	if _, err := sqsadapter.NewManagedClient(t.Context(), cfg, cfg); err == nil {
		t.Fatal("runtime started without provisioned queues")
	}
	for _, name := range []string{names.Wager, names.Events, names.DLQ} {
		_, err := admin.GetQueueUrl(t.Context(), &sqs.GetQueueUrlInput{QueueName: aws.String(name)})
		var missing *types.QueueDoesNotExist
		if !errors.As(err, &missing) {
			t.Fatalf("runtime created queue %s or unexpected error: %v", name, err)
		}
	}
	queues, err := sqsadapter.ProvisionNamed(t.Context(), admin, names)
	if err != nil {
		t.Fatal(err)
	}
	// Re-running the administrative job preserves existing messages and URLs.
	_, err = admin.SendMessage(t.Context(), &sqs.SendMessageInput{
		QueueUrl: aws.String(queues.EventsQueueURL), MessageBody: aws.String("retained-event"),
		MessageGroupId: aws.String("fixture"), MessageDeduplicationId: aws.String(uuid.NewString()),
	})
	if err != nil {
		t.Fatal(err)
	}
	again, err := sqsadapter.ProvisionNamed(t.Context(), admin, names)
	if err != nil || again != queues {
		t.Fatalf("non-idempotent provisioning: %v %v", again, err)
	}
	managed, err := sqsadapter.NewManagedClient(t.Context(), cfg, cfg)
	if err != nil {
		t.Fatal(err)
	}
	messages, err := admin.ReceiveMessage(t.Context(), &sqs.ReceiveMessageInput{QueueUrl: aws.String(queues.EventsQueueURL), WaitTimeSeconds: 1})
	if err != nil || len(messages.Messages) != 1 || aws.ToString(messages.Messages[0].Body) != "retained-event" {
		t.Fatalf("provisioning lost event: %v %v", messages, err)
	}
	_, err = admin.SetQueueAttributes(t.Context(), &sqs.SetQueueAttributesInput{
		QueueUrl: aws.String(queues.WagerQueueURL), Attributes: map[string]string{"VisibilityTimeout": "1", "RedrivePolicy": ""},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := managed.Ready(t.Context()); err == nil {
		t.Fatal("readiness accepted incompatible attributes")
	}
	if _, err := sqsadapter.NewManagedClient(t.Context(), cfg, cfg); err == nil {
		t.Fatal("runtime repaired incompatible attributes or accepted them")
	}
	attrs, err := admin.GetQueueAttributes(t.Context(), &sqs.GetQueueAttributesInput{
		QueueUrl: aws.String(queues.WagerQueueURL), AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameAll},
	})
	if err != nil || attrs.Attributes["VisibilityTimeout"] != "1" || attrs.Attributes["RedrivePolicy"] != "" {
		t.Fatalf("runtime changed attributes: %v %v", attrs, err)
	}
	if _, err := sqsadapter.ProvisionNamed(t.Context(), admin, names); err != nil {
		t.Fatal(err)
	}
	if err := managed.Ready(t.Context()); err != nil {
		t.Fatalf("administrative repair did not restore readiness: %v", err)
	}
	if _, err := admin.DeleteQueue(t.Context(), &sqs.DeleteQueueInput{QueueUrl: aws.String(queues.EventsQueueURL)}); err != nil {
		t.Fatal(err)
	}
	if err := managed.Ready(t.Context()); err == nil {
		t.Fatal("readiness accepted a deleted events queue")
	}
}
