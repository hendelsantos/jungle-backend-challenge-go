package sqs_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqsadapter "github.com/hendelsantos/jungle-backend-challenge-go/internal/adapters/sqs"
)

// This is an SDK wire-contract test, NOT a broker IAM enforcement test.
func TestRuntimeReadsWithIndependentProfiles(t *testing.T) {
	file := isolatedCredentials(t)
	if err := os.WriteFile(file, []byte("[consumer]\naws_access_key_id=local-consumer\naws_secret_access_key=fixture\n[publisher]\naws_access_key_id=local-publisher\naws_secret_access_key=fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Explicit profiles must win over an ambient administrative identity.
	t.Setenv("AWS_ACCESS_KEY_ID", "ambient-admin")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "fixture")
	var forbidden atomic.Int32
	var sends atomic.Int32
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		name, _ := req["QueueName"].(string)
		if name == "" {
			name, _ = req["QueueUrl"].(string)
		}
		identity := "local-consumer"
		if strings.Contains(name, "wager-events") {
			identity = "local-publisher"
		}
		if !strings.Contains(r.Header.Get("Authorization"), "Credential="+identity+"/") {
			t.Error("wrong runtime identity")
		}
		switch r.Header.Get("X-Amz-Target") {
		case "AmazonSQS.GetQueueUrl":
			fmt.Fprintf(w, `{"QueueUrl":%q}`, "http://"+r.Host+"/"+name)
		case "AmazonSQS.GetQueueAttributes":
			attrs := map[string]string{"FifoQueue": "true", "ContentBasedDeduplication": "false", "QueueArn": "arn:dlq"}
			if strings.Contains(name, "wager-transactions.fifo") {
				attrs["VisibilityTimeout"] = "30"
				attrs["RedrivePolicy"] = `{"deadLetterTargetArn":"arn:dlq","maxReceiveCount":"3"}`
			}
			json.NewEncoder(w).Encode(map[string]any{"Attributes": attrs})
		case "AmazonSQS.SendMessage":
			sends.Add(1)
			fmt.Fprint(w, `{}`)
		default:
			forbidden.Add(1)
			w.WriteHeader(403)
			fmt.Fprint(w, `{"__type":"AccessDenied","message":"administration forbidden"}`)
		}
	}))
	defer broker.Close()
	consumer := sqsadapter.Config{EndpointURL: broker.URL, Region: "us-east-1", Profile: "consumer"}
	publisher := consumer
	publisher.Profile = "publisher"
	runtime, err := sqsadapter.NewManagedClient(t.Context(), consumer, publisher)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Client == runtime.Publisher {
		t.Fatal("clients are shared")
	}
	if err := runtime.Ready(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, target := range []struct {
		client *sqs.Client
		url    string
	}{{runtime.Client, runtime.Queues.DLQQueueURL}, {runtime.Publisher, runtime.Queues.EventsQueueURL}} {
		if _, err := target.client.SendMessage(t.Context(), &sqs.SendMessageInput{QueueUrl: aws.String(target.url), MessageBody: aws.String("fixture")}); err != nil {
			t.Fatal(err)
		}
	}
	if forbidden.Load() != 0 || sends.Load() != 2 {
		t.Fatalf("forbidden=%d sends=%d", forbidden.Load(), sends.Load())
	}
}

func TestRuntimeFailsOnDeniedMissingAndNetwork(t *testing.T) {
	isolatedCredentials(t)
	t.Setenv("AWS_ACCESS_KEY_ID", "fixture")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "fixture")
	for _, code := range []string{"AccessDenied", "InvalidClientTokenId", "AWS.SimpleQueueService.NonExistentQueue"} {
		t.Run(code, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/x-amz-json-1.0")
				w.WriteHeader(403)
				fmt.Fprintf(w, `{"__type":%q,"message":"fixture rejection"}`, code)
			}))
			defer server.Close()
			cfg := sqsadapter.Config{EndpointURL: server.URL, Region: "us-east-1"}
			if _, err := sqsadapter.NewManagedClient(t.Context(), cfg, cfg); err == nil || !strings.Contains(err.Error(), code) {
				t.Fatalf("expected %s, got %v", code, err)
			}
		})
	}
	server := httptest.NewServer(http.NotFoundHandler())
	server.Close()
	cfg := sqsadapter.Config{EndpointURL: server.URL, Region: "us-east-1"}
	if _, err := sqsadapter.NewManagedClient(t.Context(), cfg, cfg); err == nil {
		t.Fatal("network error ignored")
	}
}
