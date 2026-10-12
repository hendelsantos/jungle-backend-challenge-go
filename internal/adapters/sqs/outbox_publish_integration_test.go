//go:build integration

// Outbox publishing end to end with real LocalStack FIFO queues and real
// PostgreSQL in an isolated schema: a committed event is delivered exactly as
// stored, a crash between send and acknowledgement is recovered with the same
// eventId, and competing publishers deliver each committed event once.
package sqs_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hendelsantos/jungle-backend-challenge-go/internal/adapters/postgres"
	sqsadapter "github.com/hendelsantos/jungle-backend-challenge-go/internal/adapters/sqs"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/command"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/observability"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/testsupport"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

type outboxFixture struct {
	client   *sqs.Client
	events   string
	pool     *pgxpool.Pool
	deps     command.PublishOutboxDeps
	metrics  *observability.OutboxMetrics
	eventIDs []uuid.UUID
}

func newOutboxFixture(t *testing.T) *outboxFixture {
	t.Helper()
	endpoint := os.Getenv("TEST_SQS_ENDPOINT")
	if endpoint == "" {
		t.Skip("TEST_SQS_ENDPOINT is not set: a real SQS broker (LocalStack) is required")
	}
	region := os.Getenv("TEST_SQS_REGION")
	if region == "" {
		region = "us-east-1"
	}
	client, err := sqsadapter.NewClient(context.Background(), sqsadapter.Config{
		EndpointURL: endpoint, Region: region,
	})
	if err != nil {
		t.Fatalf("provision queues: %v", err)
	}
	suffix := uuid.NewString()
	queues, err := sqsadapter.ProvisionNamed(context.Background(), client, sqsadapter.QueueNames{
		Wager:  "test-outbox-wager-" + suffix + ".fifo",
		DLQ:    "test-outbox-dlq-" + suffix + ".fifo",
		Events: "test-outbox-events-" + suffix + ".fifo",
	})
	if err != nil {
		t.Fatal(err)
	}
	// The publisher drains the whole committed outbox of its schema, so the
	// tests run on their own schema to stay isolated from other suites.
	pool := testsupport.IsolatedPostgresPool(t)
	metrics := observability.NewOutboxMetrics()
	deps := command.PublishOutboxDeps{
		Outbox:    postgres.NewOutboxDeliveryRepository(pool),
		Publisher: &sqsadapter.EventPublisher{Client: client, QueueURL: queues.EventsQueueURL},
		Metrics:   metrics,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, url := range []string{queues.WagerQueueURL, queues.DLQQueueURL, queues.EventsQueueURL} {
			_, _ = client.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: aws.String(url)})
		}
	})
	return &outboxFixture{client: client, events: queues.EventsQueueURL, pool: pool, deps: deps, metrics: metrics}
}

// seedEvent writes a committed event row, as a financial transaction would.
func (f *outboxFixture) seedEvent(t *testing.T) uuid.UUID {
	t.Helper()
	eventID, aggregateID := uuid.New(), uuid.New()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	payload := `{"eventId":"` + eventID.String() + `","aggregateId":"` + aggregateID.String() + `"}`
	if _, err := f.pool.Exec(ctx, `INSERT INTO outbox_events (
		event_id, aggregate_id, event_type, payload_json, occurred_at, next_attempt_at, created_at
	) VALUES ($1,$2,'WalletBalanceChanged',$3,$4,$4,$4)`,
		eventID, aggregateID, []byte(payload), time.Now().UTC()); err != nil {
		t.Fatalf("seed outbox event: %v", err)
	}
	f.eventIDs = append(f.eventIDs, eventID)
	return eventID
}

func (f *outboxFixture) receiveAll(t *testing.T, want int) []types.Message {
	t.Helper()
	var received []types.Message
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && len(received) < want {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		out, err := f.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:            aws.String(f.events),
			MaxNumberOfMessages: 10,
			WaitTimeSeconds:     2,
			MessageSystemAttributeNames: []types.MessageSystemAttributeName{
				types.MessageSystemAttributeNameMessageDeduplicationId,
				types.MessageSystemAttributeNameMessageGroupId,
			},
		})
		cancel()
		if err != nil {
			t.Fatalf("receive events: %v", err)
		}
		received = append(received, out.Messages...)
	}
	return received
}

func TestOutboxPublisherDeliversCommittedEventIntegration(t *testing.T) {
	f := newOutboxFixture(t)
	eventID, aggregateID := uuid.New(), uuid.New()
	payload := `{"eventId":"` + eventID.String() + `","aggregateId":"` + aggregateID.String() + `"}`
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := f.pool.Exec(ctx, `INSERT INTO outbox_events (
		event_id, aggregate_id, event_type, payload_json, occurred_at, next_attempt_at, created_at
	) VALUES ($1,$2,'WalletBalanceChanged',$3,$4,$4,$4)`,
		eventID, aggregateID, []byte(payload), time.Now().UTC()); err != nil {
		t.Fatalf("seed outbox event: %v", err)
	}
	// jsonb normalizes whitespace, so the published body is the committed
	// snapshot as PostgreSQL stored it, not the seeded literal.
	var committed []byte
	if err := f.pool.QueryRow(ctx, `SELECT payload_json FROM outbox_events WHERE event_id=$1`, eventID).Scan(&committed); err != nil {
		t.Fatalf("read committed payload: %v", err)
	}

	claimedBase := testutil.ToFloat64(f.metrics.Claimed)
	publishedBase := testutil.ToFloat64(f.metrics.Published)
	found, err := f.deps.PublishNext(context.Background())
	if err != nil || !found {
		t.Fatalf("publish: found=%v err=%v", found, err)
	}

	messages := f.receiveAll(t, 1)
	if len(messages) != 1 {
		t.Fatalf("delivered %d messages, want 1", len(messages))
	}
	if body := aws.ToString(messages[0].Body); body != string(committed) {
		t.Fatalf("body %q != committed payload %q", body, committed)
	}
	if got := messages[0].Attributes[string(types.MessageSystemAttributeNameMessageDeduplicationId)]; got != eventID.String() {
		t.Fatalf("dedup id %q != event id %q", got, eventID)
	}
	if got := messages[0].Attributes[string(types.MessageSystemAttributeNameMessageGroupId)]; got != aggregateID.String() {
		t.Fatalf("group id %q != aggregate id %q", got, aggregateID)
	}

	stats, err := f.deps.Outbox.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Pending != 0 || stats.OldestAgeSeconds != 0 {
		t.Fatalf("stats after publish: %+v", stats)
	}
	if got := testutil.ToFloat64(f.metrics.Published) - publishedBase; got != 1 || testutil.ToFloat64(f.metrics.Claimed)-claimedBase != 1 {
		t.Fatalf("metrics deltas claimed=%v published=%v", testutil.ToFloat64(f.metrics.Claimed)-claimedBase, got)
	}

	if found, _ := f.deps.PublishNext(context.Background()); found {
		t.Fatal("published an already acknowledged event")
	}
}

func TestOutboxPublisherRepublishesSameEventIdAfterCrashIntegration(t *testing.T) {
	f := newOutboxFixture(t)
	eventID := f.seedEvent(t)

	// Simulate a crash after SQS accepted the message and before the
	// acknowledgement: claim, send to the real broker, never mark published.
	store := f.deps.Outbox
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	claimed, found, err := store.ClaimNext(ctx, "crashed-publisher", 50*time.Millisecond)
	if err != nil || !found {
		t.Fatalf("claim: found=%v err=%v", found, err)
	}
	if err := f.deps.Publisher.Publish(ctx, claimed); err != nil {
		t.Fatalf("send before crash: %v", err)
	}

	time.Sleep(80 * time.Millisecond)
	claimedBase := testutil.ToFloat64(f.metrics.Claimed)
	publishedBase := testutil.ToFloat64(f.metrics.Published)
	found, err = f.deps.PublishNext(context.Background())
	if err != nil || !found {
		t.Fatalf("recovery publish: found=%v err=%v", found, err)
	}

	messages := f.receiveAll(t, 1)
	if len(messages) != 1 {
		t.Fatalf("delivered %d messages, want 1", len(messages))
	}
	if body := aws.ToString(messages[0].Body); body != string(claimed.Payload) {
		t.Fatalf("recovered body %q != original payload %q", body, claimed.Payload)
	}
	if got := messages[0].Attributes[string(types.MessageSystemAttributeNameMessageDeduplicationId)]; got != eventID.String() {
		t.Fatalf("recovered dedup id %q != original event id %q", got, eventID)
	}
	stats, err := store.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Pending != 0 {
		t.Fatalf("pending after recovery: %d", stats.Pending)
	}
	// The manual crash claim bypasses the command metrics by design; the
	// retry is proven by the database attempt count instead.
	var attempts int
	if err := f.pool.QueryRow(ctx, `SELECT attempts FROM outbox_events WHERE event_id=$1`, eventID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("attempts after recovery: %d", attempts)
	}
	if got := testutil.ToFloat64(f.metrics.Published) - publishedBase; got != 1 || testutil.ToFloat64(f.metrics.Claimed)-claimedBase != 1 {
		t.Fatalf("metrics deltas claimed=%v published=%v", testutil.ToFloat64(f.metrics.Claimed)-claimedBase, got)
	}
}

func TestOutboxPublisherTwoWorkersDeliverEachEventOnceIntegration(t *testing.T) {
	f := newOutboxFixture(t)
	first, second := f.seedEvent(t), f.seedEvent(t)

	const workers = 4
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				found, err := f.deps.PublishNext(context.Background())
				if err != nil {
					t.Errorf("worker publish: %v", err)
					return
				}
				if !found {
					return
				}
			}
		}()
	}
	wg.Wait()

	messages := f.receiveAll(t, 2)
	if len(messages) != 2 {
		t.Fatalf("delivered %d messages, want 2", len(messages))
	}
	seen := map[string]bool{}
	for _, message := range messages {
		dedup := message.Attributes[string(types.MessageSystemAttributeNameMessageDeduplicationId)]
		if seen[dedup] {
			t.Errorf("event %s delivered twice", dedup)
		}
		seen[dedup] = true
	}
	if !seen[first.String()] || !seen[second.String()] {
		t.Fatalf("delivered ids %v, want both seeded events", seen)
	}
	stats, err := f.deps.Outbox.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Pending != 0 {
		t.Fatalf("pending after drain: %d", stats.Pending)
	}
}
