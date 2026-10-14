//go:build integration

// SQS consumer integration against real LocalStack FIFO queues and real
// PostgreSQL: consumption commits inbox with the wager, redelivery never
// duplicates, shared command then SQS replays, invalid envelopes dead-letter,
// pending references conclude their message, and an injected failure between commit and
// delete resolves as an inbox replay.
package sqs_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
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
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/ports"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/money"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wagering"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/testsupport"
)

const testSigningKey = "local-provider-a-signing-key-32-bytes-minimum"

type sqsFixture struct {
	client   *sqs.Client
	queues   sqsadapter.Provisioned
	pool     *pgxpool.Pool
	consume  command.ConsumeWagerDeps
	process  command.ProcessWagerDeps
	create   command.CreateWalletDeps
	consumer *sqsadapter.Consumer
}

func newSQSFixture(t *testing.T) *sqsFixture {
	t.Helper()
	endpoint := os.Getenv("TEST_SQS_ENDPOINT")
	if endpoint == "" {
		t.Skip("TEST_SQS_ENDPOINT is not set: a real SQS broker (LocalStack) is required")
	}
	region := os.Getenv("TEST_SQS_REGION")
	if region == "" {
		region = "us-east-1"
	}
	pool := testsupport.PostgresPoolOrSkip(t)
	client, err := sqsadapter.NewClient(context.Background(), sqsadapter.Config{
		EndpointURL: endpoint, Region: region,
	})
	if err != nil {
		t.Fatalf("provision queues: %v", err)
	}
	suffix := uuid.NewString()
	queues, err := sqsadapter.ProvisionNamed(context.Background(), client, sqsadapter.QueueNames{
		Wager: "test-wager-" + suffix + ".fifo", DLQ: "test-dlq-" + suffix + ".fifo", Events: "test-events-" + suffix + ".fifo",
	})
	if err != nil {
		t.Fatal(err)
	}
	managed := &sqsadapter.ManagedClient{Client: client, Queues: queues}
	reader := postgres.NewWagerTransactionReader(pool)
	consume := command.ConsumeWagerDeps{
		UnitOfWork: postgres.NewUnitOfWork(pool),
		Process: command.ProcessWagerDeps{
			UnitOfWork:   postgres.NewUnitOfWork(pool),
			Wallets:      postgres.NewWalletRepository(),
			Wagers:       postgres.NewWagerTransactionRepository(),
			WagerLookups: reader,
			Ledger:       postgres.NewLedgerRepository(),
			Outbox:       postgres.NewOutboxRepository(),
			Now:          func() time.Time { return time.Now().UTC() },
			NewID:        uuid.New,
		},
		Inbox: postgres.NewInboxRepository(),
		Now:   func() time.Time { return time.Now().UTC() },
	}
	create := command.CreateWalletDeps{
		UnitOfWork: postgres.NewUnitOfWork(pool),
		Wallets:    postgres.NewWalletRepository(),
		Wagers:     postgres.NewWagerTransactionRepository(),
		Ledger:     postgres.NewLedgerRepository(),
		Outbox:     postgres.NewOutboxRepository(),
		Now:        func() time.Time { return time.Now().UTC() },
		NewID:      uuid.New,
	}
	consumer := &sqsadapter.Consumer{
		Consume:       consume.ConsumeWagerMessage,
		Client:        managed.Client,
		WagerQueueURL: managed.Queues.WagerQueueURL,
		DLQQueueURL:   managed.Queues.DLQQueueURL,
		ConsumerName:  "wager-consumer",
		PollWait:      1,
		ProviderKeys:  sqsadapter.ProviderKeys{"provider-a": testSigningKey},
	}
	f := &sqsFixture{
		client: managed.Client, queues: managed.Queues, pool: pool,
		consume: consume, process: consume.Process, create: create, consumer: consumer,
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, url := range []string{managed.Queues.WagerQueueURL, managed.Queues.DLQQueueURL, managed.Queues.EventsQueueURL} {
			_, _ = f.client.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: aws.String(url)})
		}
	})
	return f
}

func (f *sqsFixture) openWallet(t *testing.T) (uuid.UUID, uuid.UUID) {
	t.Helper()
	balance, err := money.ParseDecimal("100.00", money.BRL)
	if err != nil {
		t.Fatal(err)
	}
	playerID := uuid.New()
	created, err := f.create.CreateWallet(context.Background(), command.CreateWalletInput{
		PlayerID: playerID, InitialBalance: balance, CorrelationID: uuid.New(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return created.WalletID, playerID
}

// sendMessage publishes one envelope with explicit group and dedup ids, as a
// trusted producer would: group per wallet for FIFO ordering, dedup per
// message identity.
func (f *sqsFixture) sendMessage(t *testing.T, body, groupID, dedupID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := f.client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:               aws.String(f.queues.WagerQueueURL),
		MessageBody:            aws.String(body),
		MessageGroupId:         aws.String(groupID),
		MessageDeduplicationId: aws.String(dedupID),
		MessageAttributes:      sqsadapter.SignAttributes("provider-a", testSigningKey, []byte(body)),
	})
	if err != nil {
		t.Fatalf("send message: %v", err)
	}
}

func envelopeBody(messageID, externalID string, walletID, playerID uuid.UUID, kind, amount string) string {
	reference := ""
	if kind == "REFUND" || kind == "ROLLBACK" {
		reference = fmt.Sprintf(`,"referenceExternalTransactionId":%q`, externalID+"-ref")
	}
	return fmt.Sprintf(`{
		"messageId": %q,
		"type": "WagerTransactionRequested",
		"occurredAt": "2026-10-12T12:00:00.000Z",
		"data": {
			"providerId": "provider-a",
			"externalTransactionId": %q,
			"idempotencyKey": "provider-a:%s",
			"playerId": %q,
			"walletId": %q,
			"roundId": "round-1",
			"gameId": "fortune-chimp",
			"kind": %q,
			"money": {"amount": %q, "currency": "BRL"}%s
		}
	}`, messageID, externalID, externalID, playerID, walletID, kind, amount, reference)
}

// runConsumerUntil starts the consumer loop and cancels it once the condition
// holds across two observations. Queue counts include invisible and delayed
// messages. The deadline allows the 30s visibility of an ambiguous cancelled
// ReceiveMessage request to expire. It also proves cancellation stops the loop.
func (f *sqsFixture) runConsumerUntil(t *testing.T, condition func() bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.consumer.Run(ctx)
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("consumer did not stop after cancellation")
		}
	}()
	deadline := time.Now().Add(45 * time.Second)
	for {
		if !condition() {
			if time.Now().After(deadline) {
				cancel()
				t.Fatal("condition not reached within the deadline")
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		// First window matched: hold the consumer running while any invisible
		// message reappears, then require the condition again.
		time.Sleep(1300 * time.Millisecond)
		if condition() {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("condition not stable within the deadline")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("consumer did not stop after cancellation")
	}
}

func (f *sqsFixture) countInbox(t *testing.T, messageID string) int64 {
	t.Helper()
	var count int64
	err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM inbox_messages WHERE message_id = $1`, messageID).Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	return count
}

func (f *sqsFixture) walletState(t *testing.T, walletID uuid.UUID) (int64, int64) {
	t.Helper()
	var balance, version int64
	err := f.pool.QueryRow(context.Background(),
		`SELECT balance_cents, version FROM wallets WHERE id = $1`, walletID).Scan(&balance, &version)
	if err != nil {
		t.Fatal(err)
	}
	return balance, version
}

func TestSQSConsumerIntegration(t *testing.T) {

	t.Run("bet via sqs commits inbox and deletes the message", func(t *testing.T) {
		f := newSQSFixture(t)
		walletID, playerID := f.openWallet(t)
		messageID := "msg-bet-" + uuid.NewString()[:8]
		body := envelopeBody(messageID, "tx-sqs-bet-"+walletID.String()[:8], walletID, playerID, "BET", "25.00")
		f.sendMessage(t, body, walletID.String(), messageID)

		f.runConsumerUntil(t, func() bool { return f.countInbox(t, messageID) == 1 })

		if balance, version := f.walletState(t, walletID); balance != 7500 || version != 2 {
			t.Fatalf("balance=%d version=%d, want 75.00 v2", balance, version)
		}
		if entries := countRows(t, f.pool,
			"SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1", walletID); entries != 2 {
			t.Fatalf("ledger entries = %d", entries)
		}
		if messages := queueMessages(t, f.client, f.queues.WagerQueueURL); messages != 0 {
			t.Fatalf("queue still holds %d messages after consumption", messages)
		}
	})

	t.Run("redelivery of the same message never duplicates", func(t *testing.T) {
		f := newSQSFixture(t)
		walletID, playerID := f.openWallet(t)
		messageID := "msg-redelivery-" + uuid.NewString()[:8]
		external := "tx-sqs-redelivery-" + walletID.String()[:8]
		body := envelopeBody(messageID, external, walletID, playerID, "BET", "25.00")
		// A redelivery carries the same body under a new transport identity:
		// the dedup window differs, the business identity does not.
		f.sendMessage(t, body, walletID.String(), messageID)
		f.sendMessage(t, body, walletID.String(), messageID+"-redelivered")

		f.runConsumerUntil(t, func() bool {
			return f.countInbox(t, messageID) == 1 && queueMessages(t, f.client, f.queues.WagerQueueURL) == 0
		})

		if balance, version := f.walletState(t, walletID); balance != 7500 || version != 2 {
			t.Fatalf("balance=%d version=%d, redelivery duplicated the bet", balance, version)
		}
		if entries := countRows(t, f.pool,
			"SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1", walletID); entries != 2 {
			t.Fatalf("ledger entries = %d", entries)
		}
		if count := f.countInbox(t, messageID); count != 1 {
			t.Fatalf("inbox rows = %d", count)
		}
	})

	t.Run("command first then sqs replays without a second effect", func(t *testing.T) {
		f := newSQSFixture(t)
		walletID, playerID := f.openWallet(t)
		messageID := "msg-cross-" + uuid.NewString()[:8]
		external := "tx-cross-" + walletID.String()[:8]
		// The HTTP boundary calls exactly this use case with these identities.
		value, err := money.ParseDecimal("25.00", money.BRL)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.process.ProcessWager(context.Background(), command.ProcessWagerInput{
			ProviderID: "provider-a", ExternalTransactionID: external,
			IdempotencyKey: "provider-a:" + external, PlayerID: playerID, WalletID: walletID,
			RoundID: "round-1", GameID: "fortune-chimp", Kind: wagering.Bet,
			Money: value, CorrelationID: uuid.New(),
		}); err != nil {
			t.Fatal(err)
		}
		f.sendMessage(t, envelopeBody(messageID, external, walletID, playerID, "BET", "25.00"),
			walletID.String(), messageID)

		f.runConsumerUntil(t, func() bool { return f.countInbox(t, messageID) == 1 })

		if balance, version := f.walletState(t, walletID); balance != 7500 || version != 2 {
			t.Fatalf("balance=%d version=%d, cross-channel duplicated the bet", balance, version)
		}
		if entries := countRows(t, f.pool,
			"SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1", walletID); entries != 2 {
			t.Fatalf("ledger entries = %d", entries)
		}
	})

	t.Run("invalid envelope is dead-lettered without evaluation", func(t *testing.T) {
		f := newSQSFixture(t)
		walletID, _ := f.openWallet(t)
		balance, _ := f.walletState(t, walletID)
		f.sendMessage(t, `{"messageId": "broken", "type":`,
			walletID.String(), "dlq-broken-"+uuid.NewString()[:8])

		f.runConsumerUntil(t, func() bool {
			return queueMessages(t, f.client, f.queues.DLQQueueURL) == 1
		})

		if current, _ := f.walletState(t, walletID); current != balance {
			t.Fatalf("invalid message moved the wallet: %d -> %d", balance, current)
		}
		if messages := queueMessages(t, f.client, f.queues.WagerQueueURL); messages != 0 {
			t.Fatalf("intake queue still holds %d messages", messages)
		}
	})

	t.Run("pending reference concludes its message", func(t *testing.T) {
		f := newSQSFixture(t)
		walletID, playerID := f.openWallet(t)
		messageID := "msg-pending-" + uuid.NewString()[:8]
		external := "tx-sqs-pending-" + walletID.String()[:8]
		f.sendMessage(t, envelopeBody(messageID, external, walletID, playerID, "REFUND", "25.00"),
			walletID.String(), messageID)

		f.runConsumerUntil(t, func() bool { return f.countInbox(t, messageID) == 1 })

		var status string
		err := f.pool.QueryRow(context.Background(),
			`SELECT status FROM wager_transactions WHERE provider_id = 'provider-a' AND external_transaction_id = $1`,
			external).Scan(&status)
		if err != nil {
			t.Fatal(err)
		}
		if status != string(wagering.PendingReference) {
			t.Fatalf("status = %s, want PENDING_REFERENCE", status)
		}
		if balance, version := f.walletState(t, walletID); balance != 10000 || version != 1 {
			t.Fatalf("pending reference moved the wallet: %d v%d", balance, version)
		}
		if messages := queueMessages(t, f.client, f.queues.WagerQueueURL); messages != 0 {
			t.Fatalf("queue still holds %d messages", messages)
		}
	})

	t.Run("fault injected between commit and delete resolves as inbox replay", func(t *testing.T) {
		f := newSQSFixture(t)
		walletID, playerID := f.openWallet(t)
		messageID := "msg-crash-" + uuid.NewString()[:8]
		external := "tx-sqs-crash-" + walletID.String()[:8]
		body := envelopeBody(messageID, external, walletID, playerID, "BET", "25.00")
		// Shorten the visibility so the redelivery arrives within the test.
		setVisibility(t, f.client, f.queues.WagerQueueURL, 1)

		realConsume := f.consumer.Consume
		var attempts atomic.Int32
		var crashErr = errors.New("simulated crash between commit and delete")
		f.consumer.Consume = func(ctx context.Context, input command.ConsumeWagerInput) (command.ConsumeWagerResult, error) {
			result, err := realConsume(ctx, input)
			if attempts.Add(1) == 1 && err == nil {
				// The commit happened; the process dies before DeleteMessage.
				return result, crashErr
			}
			return result, err
		}
		t.Cleanup(func() { f.consumer.Consume = realConsume })

		f.sendMessage(t, body, walletID.String(), messageID)
		// First pass commits but "crashes" before deleting; redelivery (1s
		// visibility) resolves as an inbox replay and deletes the message.
		f.runConsumerUntil(t, func() bool {
			return attempts.Load() >= 2 && f.countInbox(t, messageID) == 1 &&
				queueMessages(t, f.client, f.queues.WagerQueueURL) == 0
		})

		if balance, version := f.walletState(t, walletID); balance != 7500 || version != 2 {
			t.Fatalf("balance=%d version=%d, crash-after-commit duplicated the bet", balance, version)
		}
		if entries := countRows(t, f.pool,
			"SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1", walletID); entries != 2 {
			t.Fatalf("ledger entries = %d", entries)
		}
		if count := f.countInbox(t, messageID); count != 1 {
			t.Fatalf("inbox rows = %d", count)
		}
	})
}

func queueMessages(t *testing.T, client *sqs.Client, url string) int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl: aws.String(url),
		AttributeNames: []types.QueueAttributeName{
			types.QueueAttributeNameApproximateNumberOfMessages,
			types.QueueAttributeNameApproximateNumberOfMessagesNotVisible,
			types.QueueAttributeNameApproximateNumberOfMessagesDelayed},
	})
	if err != nil {
		t.Fatal(err)
	}
	var count int64
	for _, key := range []string{"ApproximateNumberOfMessages", "ApproximateNumberOfMessagesNotVisible", "ApproximateNumberOfMessagesDelayed"} {
		n, err := strconv.ParseInt(out.Attributes[key], 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		count += n
	}
	return count
}

func setVisibility(t *testing.T, client *sqs.Client, url string, seconds int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := client.SetQueueAttributes(ctx, &sqs.SetQueueAttributesInput{
		QueueUrl:   aws.String(url),
		Attributes: map[string]string{"VisibilityTimeout": fmt.Sprint(seconds)},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func countRows(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int64 {
	t.Helper()
	var count int64
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// Fail the real inbox INSERT after the financial statements ran. PostgreSQL's
// CHECK constraint, rather than an in-memory rollback fake, aborts the commit.
type invalidCompletionInbox struct {
	real  ports.InboxRepository
	calls atomic.Int32
}

func (i *invalidCompletionInbox) TryCompleteInbox(ctx context.Context, completion ports.InboxCompletion) (bool, error) {
	i.calls.Add(1)
	completion.CompletedAt = completion.ReceivedAt.Add(-time.Second)
	return i.real.TryCompleteInbox(ctx, completion)
}

func TestSQSInboxRollbackAndBrokerRedriveIntegration(t *testing.T) {
	f := newSQSFixture(t)
	walletID, playerID := f.openWallet(t)
	badInbox := &invalidCompletionInbox{real: postgres.NewInboxRepository()}
	f.consume.Inbox = badInbox
	f.consumer.Consume = f.consume.ConsumeWagerMessage
	messageID, external := uuid.NewString(), uuid.NewString()
	f.sendMessage(t, envelopeBody(messageID, external, walletID, playerID, "BET", "25.00"), walletID.String(), messageID)
	f.runConsumerUntil(t, func() bool {
		return queueMessages(t, f.client, f.queues.DLQQueueURL) == 1 && queueMessages(t, f.client, f.queues.WagerQueueURL) == 0
	})
	if badInbox.calls.Load() != 3 {
		t.Fatalf("attempts=%d want redrive after 3", badInbox.calls.Load())
	}
	if balance, version := f.walletState(t, walletID); balance != 10000 || version != 1 {
		t.Fatalf("partial commit: %d v%d", balance, version)
	}
	if f.countInbox(t, messageID) != 0 {
		t.Fatal("inbox committed despite failed CHECK")
	}
	if n := countRows(t, f.pool, "SELECT count(*) FROM wager_transactions WHERE external_transaction_id=$1", external); n != 0 {
		t.Fatal("wager survived rollback")
	}
	if n := countRows(t, f.pool, "SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id=$1", walletID); n != 1 {
		t.Fatal("ledger survived rollback")
	}
	if n := countRows(t, f.pool, "SELECT count(*) FROM outbox_events WHERE payload_json->'data'->>'walletId'=$1", walletID.String()); n != 2 {
		t.Fatalf("outbox=%d, want only two opening events", n)
	}
}

type ambiguousCommit struct {
	real     ports.UnitOfWork
	returned atomic.Bool
}

func (u *ambiguousCommit) WithinTx(ctx context.Context, fn func(context.Context) error) error {
	err := u.real.WithinTx(ctx, fn)
	if err == nil && u.returned.CompareAndSwap(false, true) {
		return errors.New("injected lost commit acknowledgement")
	}
	return err
}
func TestSQSUnknownCommitResultReplaysIntegration(t *testing.T) {
	f := newSQSFixture(t)
	f.consume.UnitOfWork = &ambiguousCommit{real: postgres.NewUnitOfWork(f.pool)}
	var attempts atomic.Int32
	var replay atomic.Bool
	f.consumer.Consume = func(ctx context.Context, in command.ConsumeWagerInput) (command.ConsumeWagerResult, error) {
		attempts.Add(1)
		result, err := f.consume.ConsumeWagerMessage(ctx, in)
		if err == nil && result.InboxReplay && result.Replay {
			replay.Store(true)
		}
		return result, err
	}
	walletID, playerID := f.openWallet(t)
	messageID := uuid.NewString()
	f.sendMessage(t, envelopeBody(messageID, uuid.NewString(), walletID, playerID, "BET", "25.00"), walletID.String(), messageID)
	f.runConsumerUntil(t, func() bool { return replay.Load() && queueMessages(t, f.client, f.queues.WagerQueueURL) == 0 })
	if attempts.Load() < 2 {
		t.Fatal("missing actual broker redelivery")
	}
	if balance, version := f.walletState(t, walletID); balance != 7500 || version != 2 {
		t.Fatalf("duplicate commit: %d v%d", balance, version)
	}
}

func TestSQSRejectsUnauthorizedAndInvalidOperationsIntegration(t *testing.T) {
	for _, scenario := range []string{"missing signature", "forged provider", "invalid amount", "OPENING", "long identifier", "multibyte identifier"} {
		t.Run(scenario, func(t *testing.T) {
			f := newSQSFixture(t)
			walletID, playerID := f.openWallet(t)
			messageID := uuid.NewString()
			body := envelopeBody(messageID, uuid.NewString(), walletID, playerID, "BET", "25.00")
			if scenario == "forged provider" {
				body = strings.ReplaceAll(body, "provider-a", "provider-b")
			}
			if scenario == "invalid amount" {
				body = strings.ReplaceAll(body, "25.00", "0.00")
			}
			if scenario == "OPENING" {
				body = strings.ReplaceAll(body, "BET", "OPENING")
			}
			if scenario == "long identifier" {
				body = strings.ReplaceAll(body, "fortune-chimp", strings.Repeat("a", 12000))
			}
			if scenario == "multibyte identifier" {
				body = strings.ReplaceAll(body, "fortune-chimp", strings.Repeat("é", 257))
			}
			attrs := sqsadapter.SignAttributes("provider-a", testSigningKey, []byte(body))
			if scenario == "missing signature" {
				attrs = nil
			}
			_, err := f.client.SendMessage(t.Context(), &sqs.SendMessageInput{QueueUrl: aws.String(f.queues.WagerQueueURL), MessageBody: aws.String(body), MessageGroupId: aws.String(walletID.String()), MessageDeduplicationId: aws.String(messageID), MessageAttributes: attrs})
			if err != nil {
				t.Fatal(err)
			}
			f.runConsumerUntil(t, func() bool {
				return queueMessages(t, f.client, f.queues.DLQQueueURL) == 1 && queueMessages(t, f.client, f.queues.WagerQueueURL) == 0
			})
			if f.countInbox(t, messageID) != 0 {
				t.Fatal("invalid message committed")
			}
			if balance, version := f.walletState(t, walletID); balance != 10000 || version != 1 {
				t.Fatalf("unauthorized effect: %d v%d", balance, version)
			}
		})
	}
}

func TestSQSRewrittenInboxIdentityRollsBackIntegration(t *testing.T) {
	f := newSQSFixture(t)
	walletID, playerID := f.openWallet(t)
	messageID := uuid.NewString()
	f.sendMessage(t, envelopeBody(messageID, uuid.NewString(), walletID, playerID, "BET", "25.00"), walletID.String(), uuid.NewString())
	f.runConsumerUntil(t, func() bool {
		return f.countInbox(t, messageID) == 1 && queueMessages(t, f.client, f.queues.WagerQueueURL) == 0
	})
	// A distinct business operation under an already completed envelope identity.
	rewrittenExternal := uuid.NewString()
	f.sendMessage(t, envelopeBody(messageID, rewrittenExternal, walletID, playerID, "BET", "10.00"), walletID.String(), uuid.NewString())
	f.runConsumerUntil(t, func() bool {
		return queueMessages(t, f.client, f.queues.DLQQueueURL) == 1 && queueMessages(t, f.client, f.queues.WagerQueueURL) == 0
	})
	if balance, version := f.walletState(t, walletID); balance != 7500 || version != 2 {
		t.Fatalf("rewritten identity committed: %d v%d", balance, version)
	}
	if n := countRows(t, f.pool, "SELECT count(*) FROM wager_transactions WHERE external_transaction_id=$1", rewrittenExternal); n != 0 {
		t.Fatal("conflicting operation committed")
	}
}

func TestSQSBusinessRejectionIsTerminalIntegration(t *testing.T) {
	f := newSQSFixture(t)
	walletID, playerID := f.openWallet(t)
	messageID, external := uuid.NewString(), uuid.NewString()
	f.sendMessage(t, envelopeBody(messageID, external, walletID, playerID, "BET", "180.00"), walletID.String(), messageID)
	f.runConsumerUntil(t, func() bool {
		return f.countInbox(t, messageID) == 1 && queueMessages(t, f.client, f.queues.WagerQueueURL) == 0
	})
	var status string
	if err := f.pool.QueryRow(t.Context(), "SELECT status FROM wager_transactions WHERE external_transaction_id=$1", external).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "REJECTED" || queueMessages(t, f.client, f.queues.DLQQueueURL) != 0 {
		t.Fatalf("rejected business outcome retried: %s", status)
	}
	if balance, version := f.walletState(t, walletID); balance != 10000 || version != 1 {
		t.Fatalf("rejection moved money: %d v%d", balance, version)
	}
}

func TestSQSShutdownRollsBackAndReleasesVisibilityIntegration(t *testing.T) {
	f := newSQSFixture(t)
	walletID, playerID := f.openWallet(t)
	messageID := uuid.NewString()
	entered := make(chan struct{})
	f.consumer.Consume = func(ctx context.Context, _ command.ConsumeWagerInput) (command.ConsumeWagerResult, error) {
		err := postgres.NewUnitOfWork(f.pool).WithinTx(ctx, func(ctx context.Context) error {
			tx, _ := postgres.TxFromContext(ctx)
			if _, err := tx.Exec(ctx, "UPDATE wallets SET balance_cents=7500 WHERE id=$1", walletID); err != nil {
				return err
			}
			close(entered)
			<-ctx.Done()
			return ctx.Err()
		})
		return command.ConsumeWagerResult{}, err
	}
	f.sendMessage(t, envelopeBody(messageID, uuid.NewString(), walletID, playerID, "BET", "25.00"), walletID.String(), messageID)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); f.consumer.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("consumer shutdown timed out")
		}
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("consumer never entered transaction")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not finish")
	}
	if balance, version := f.walletState(t, walletID); balance != 10000 || version != 1 {
		t.Fatalf("cancelled work committed: %d v%d", balance, version)
	}
	if f.countInbox(t, messageID) != 0 {
		t.Fatal("cancelled inbox committed")
	}
	// Immediately receive again, without waiting for the original 30s visibility.
	received, err := f.client.ReceiveMessage(t.Context(), &sqs.ReceiveMessageInput{QueueUrl: aws.String(f.queues.WagerQueueURL), WaitTimeSeconds: 1, MaxNumberOfMessages: 1})
	if err != nil || len(received.Messages) != 1 {
		t.Fatalf("visibility not released: %v %v", received, err)
	}
}

func TestSQSProvisionRepairsExistingRedriveIntegration(t *testing.T) {
	f := newSQSFixture(t)
	_, err := f.client.SetQueueAttributes(t.Context(), &sqs.SetQueueAttributesInput{QueueUrl: aws.String(f.queues.WagerQueueURL), Attributes: map[string]string{"RedrivePolicy": "", "VisibilityTimeout": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	// Resolve names from URLs without assuming a particular LocalStack URL strategy.
	name := func(url string) string { parts := strings.Split(url, "/"); return parts[len(parts)-1] }
	queues, err := sqsadapter.ProvisionNamed(t.Context(), f.client, sqsadapter.QueueNames{Wager: name(f.queues.WagerQueueURL), DLQ: name(f.queues.DLQQueueURL), Events: name(f.queues.EventsQueueURL)})
	if err != nil {
		t.Fatal(err)
	}
	attributes, err := f.client.GetQueueAttributes(t.Context(), &sqs.GetQueueAttributesInput{QueueUrl: aws.String(queues.WagerQueueURL), AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameAll}})
	if err != nil {
		t.Fatal(err)
	}
	if attributes.Attributes["VisibilityTimeout"] != "30" || !strings.Contains(attributes.Attributes["RedrivePolicy"], "maxReceiveCount") || attributes.Attributes["FifoQueue"] != "true" {
		t.Fatalf("incorrect queue policy: %v", attributes.Attributes)
	}
}
