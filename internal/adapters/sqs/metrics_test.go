package sqs

import (
	"context"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/command"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wagering"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/observability"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"net/http"
	"net/http/httptest"
	"testing"
)

func metricValue(c prometheus.Metric) float64 {
	var m dto.Metric
	_ = c.Write(&m)
	return m.GetCounter().GetValue()
}
func TestConsumerMetricsDistinguishReplayAndDLQFailure(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_SESSION_TOKEN", "")
	rejectSend := false
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		if rejectSend && r.Header.Get("X-Amz-Target") == "AmazonSQS.SendMessage" {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"__type":"AccessDeniedException","message":"denied"}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer broker.Close()
	client, err := NewClient(context.Background(), Config{EndpointURL: broker.URL, Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	metrics := observability.NewFinancialMetrics()
	key := "test-signing-key-a-at-least-32-bytes"
	c := Consumer{Client: client, WagerQueueURL: broker.URL + "/input", DLQQueueURL: broker.URL + "/dlq", ConsumerName: "wager-consumer", ProviderKeys: ProviderKeys{"provider-a": key}, Metrics: metrics}
	replay := false
	c.Consume = func(context.Context, command.ConsumeWagerInput) (command.ConsumeWagerResult, error) {
		return command.ConsumeWagerResult{Status: wagering.Processed, Replay: replay, InboxReplay: replay}, nil
	}
	message := types.Message{MessageId: aws.String("broker-id"), ReceiptHandle: aws.String("receipt"), Body: aws.String(validEnvelope), MessageAttributes: SignAttributes("provider-a", key, []byte(validEnvelope))}
	op := metrics.Operations.WithLabelValues("BET", "PROCESSED")
	before, replays := metricValue(op), metricValue(metrics.Replays)
	c.handle(context.Background(), message)
	replay = true
	c.handle(context.Background(), message)
	if metricValue(op)-before != 1 || metricValue(metrics.Replays)-replays != 1 {
		t.Fatal("replay counted as financial operation")
	}
	dlq, failures := metricValue(metrics.Dlq), metricValue(metrics.DlqFailures)
	message.MessageAttributes = nil
	rejectSend = true
	c.handle(context.Background(), message)
	if metricValue(metrics.Dlq) != dlq || metricValue(metrics.DlqFailures)-failures != 1 {
		t.Fatal("failed DLQ copy counted as delivered")
	}
	rejectSend = false
	c.handle(context.Background(), message)
	if metricValue(metrics.Dlq)-dlq != 1 {
		t.Fatal("confirmed DLQ copy not counted")
	}
}
