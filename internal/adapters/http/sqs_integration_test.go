//go:build integration

package httpapi_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/google/uuid"
	sqsadapter "github.com/hendelsantos/jungle-backend-challenge-go/internal/adapters/sqs"
)

func TestAuthenticatedHTTPAndSQSIntegration(t *testing.T) {
	endpoint := os.Getenv("TEST_SQS_ENDPOINT")
	if endpoint == "" {
		t.Skip("TEST_SQS_ENDPOINT is not set: real LocalStack required")
	}
	const key = "local-provider-a-signing-key-32-bytes-minimum"
	prefix := "http-" + uuid.NewString()[:8] + "-"
	t.Setenv("SQS_ENDPOINT_URL", endpoint)
	t.Setenv("SQS_PROVIDER_KEYS", `{"provider-a":"`+key+`"}`)
	t.Setenv("SQS_QUEUE_PREFIX", prefix)
	client, err := sqsadapter.NewClient(t.Context(), sqsadapter.Config{EndpointURL: endpoint, Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	// Administrative setup is explicit; Fx runtime must never create queues.
	if _, err := sqsadapter.ProvisionNamed(t.Context(), client, (sqsadapter.Config{QueuePrefix: prefix}).QueueNames()); err != nil {
		t.Fatal(err)
	}
	// Registered before the Fx fixture cleanup: workers stop before queue deletion.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, name := range []string{sqsadapter.WagerQueueName, sqsadapter.DLQQueueName, sqsadapter.EventsQueueName} {
			found, err := client.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: aws.String(prefix + name)})
			if err == nil {
				_, _ = client.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: found.QueueUrl})
			}
		}
	})
	f := newHTTPFixture(t)
	found, err := client.GetQueueUrl(t.Context(), &sqs.GetQueueUrlInput{QueueName: aws.String(prefix + sqsadapter.WagerQueueName)})
	if err != nil {
		t.Fatal(err)
	}
	status, ready := f.request(t, "GET", "/health/ready", "", "", nil)
	checkStatus(t, status, 200, ready)
	var checks map[string]string
	if err := json.Unmarshal(ready["checks"], &checks); err != nil || checks["sqs"] != "ok" {
		t.Fatalf("SQS readiness: %v %v", checks, err)
	}
	for _, order := range []string{"http-first", "sqs-first", "concurrent"} {
		t.Run(order, func(t *testing.T) {
			wallet, player := f.wallet(t, "100.00")
			input := wager(wallet, player, "BET", "25.00")
			businessKey := "provider-a:" + input["externalTransactionId"].(string)
			httpBody := encode(t, input)
			input["idempotencyKey"] = businessKey
			messageID := uuid.NewString()
			body := encode(t, map[string]any{"messageId": messageID, "type": "WagerTransactionRequested", "occurredAt": time.Now().UTC().Format(time.RFC3339), "data": input})
			send := func() error {
				_, err := client.SendMessage(t.Context(), &sqs.SendMessageInput{QueueUrl: found.QueueUrl, MessageBody: aws.String(string(body)), MessageGroupId: aws.String(wallet), MessageDeduplicationId: aws.String(messageID), MessageAttributes: sqsadapter.SignAttributes("provider-a", key, body)})
				return err
			}
			waitInbox := func() {
				deadline := time.Now().Add(15 * time.Second)
				for {
					var complete bool
					if err := f.pool.QueryRow(t.Context(), "SELECT EXISTS(SELECT 1 FROM inbox_messages WHERE consumer_name='wager-consumer' AND message_id=$1 AND completed_at IS NOT NULL)", messageID).Scan(&complete); err != nil {
						t.Fatal(err)
					}
					if complete {
						return
					}
					if time.Now().After(deadline) {
						t.Fatal("SQS message not committed")
					}
					time.Sleep(20 * time.Millisecond)
				}
			}
			switch order {
			case "sqs-first":
				if err := send(); err != nil {
					t.Fatal(err)
				}
				waitInbox()
			case "concurrent":
				// The broker and HTTP socket are independent transports, using one business identity.
				sent := make(chan error, 1)
				go func() { sent <- send() }()
				status, result := f.request(t, "POST", "/wagering/transactions", f.tokens["provider-a-client"], businessKey, httpBody)
				checkStatus(t, status, 200, result)
				if err := <-sent; err != nil {
					t.Fatal(err)
				}
			}
			if order != "concurrent" {
				status, result := f.request(t, "POST", "/wagering/transactions", f.tokens["provider-a-client"], businessKey, httpBody)
				checkStatus(t, status, 200, result)
			}
			if order == "http-first" {
				if err := send(); err != nil {
					t.Fatal(err)
				}
			}
			waitInbox()
			if got := f.effects(t, wallet); got != [5]int64{7500, 2, 2, 2, 4} {
				t.Fatalf("duplicate financial effects: %v", got)
			}
			status, result := f.request(t, "POST", "/wallets/"+wallet+"/reconciliation", f.tokens["wallet-internal-client"], "", nil)
			checkStatus(t, status, 200, result)
			if string(result["consistent"]) != "true" {
				t.Fatalf("ledger mismatch: %s", encode(t, result))
			}
		})
	}
	// Loss of a real queue must turn dependency readiness into 503.
	eventQueue, err := client.GetQueueUrl(t.Context(), &sqs.GetQueueUrlInput{QueueName: aws.String(prefix + sqsadapter.EventsQueueName)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.DeleteQueue(t.Context(), &sqs.DeleteQueueInput{QueueUrl: eventQueue.QueueUrl}); err != nil {
		t.Fatal(err)
	}
	status, ready = f.request(t, "GET", "/health/ready", "", "", nil)
	checkStatus(t, status, 503, ready)
}
