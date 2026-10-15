//go:build awshomolog

// Acceptance probes against REAL managed AWS SQS in an explicitly disposable
// homologation account/region. They prove broker-side authorization (A01) and
// redelivery durability (A02) against the service itself — never an emulator.
// The CI only compiles this package; execution requires the homologation
// environment (docs/HOMOLOGACAO_AWS.md) and explicit env. Never run against
// shared or production resources: every queue/role carries the homolog prefix.
package homolog_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hendelsantos/jungle-backend-challenge-go/internal/adapters/postgres"
	sqsadapter "github.com/hendelsantos/jungle-backend-challenge-go/internal/adapters/sqs"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/command"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/money"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wagering"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/observability"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/workers"
)

// Fictional homologation signing key (>=32 bytes), used only by this probe's
// in-process consumer. Not a credential; the broker trust boundary is IAM.
const providerKey = "homolog-provider-a-signing-key-32b-min"

// transport observes the broker verdict and restricts egress to AWS HTTPS.
type transport struct{ status atomic.Int32 }

func (r *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" || !strings.HasSuffix(req.URL.Hostname(), ".amazonaws.com") {
		return nil, fmt.Errorf("homolog probes permit only AWS HTTPS endpoints, got %s", req.URL)
	}
	res, err := http.DefaultTransport.RoundTrip(req)
	if res != nil {
		r.status.Store(int32(res.StatusCode))
	}
	return res, err
}

type environment struct {
	account string
	region  string
	prefix  string
	base    aws.Config
}

func env(t *testing.T) environment {
	t.Helper()
	if os.Getenv("AWS_HOMOLOG_DISPOSABLE") != "1" {
		t.Skip("AWS_HOMOLOG_DISPOSABLE=1 required: probes operate on disposable homolog resources")
	}
	account := os.Getenv("JUNGLE_HOMOLOG_ACCOUNT")
	region := os.Getenv("JUNGLE_HOMOLOG_REGION")
	prefix := os.Getenv("JUNGLE_HOMOLOG_QUEUE_PREFIX")
	if !regexp.MustCompile(`^[0-9]{12}$`).MatchString(account) ||
		!regexp.MustCompile(`^[a-z]{2}-[a-z-]+-[0-9]$`).MatchString(region) ||
		!regexp.MustCompile(`^homolog-[a-z0-9-]+-$`).MatchString(prefix) {
		t.Skip("JUNGLE_HOMOLOG_ACCOUNT (12 digits), JUNGLE_HOMOLOG_REGION and JUNGLE_HOMOLOG_QUEUE_PREFIX (homolog-*-) are required")
	}
	cfg, err := awsconfig.LoadDefaultConfig(t.Context(), awsconfig.WithRegion(region))
	if err != nil {
		t.Fatalf("load AWS base configuration: %v", err)
	}
	if _, err := cfg.Credentials.Retrieve(t.Context()); err != nil {
		t.Fatalf("base AWS identity is not configured (set AWS_PROFILE or ambient credentials): %v", err)
	}
	return environment{account: account, region: region, prefix: prefix, base: cfg}
}

type identity struct {
	name   string
	client *sqs.Client
	wire   *transport
}

func newClient(t *testing.T, name string, e environment, provider aws.CredentialsProvider) identity {
	t.Helper()
	wire := &transport{}
	cfg := aws.Config{Region: e.region, Credentials: provider, RetryMaxAttempts: 1, HTTPClient: &http.Client{Transport: wire, Timeout: 20 * time.Second}}
	return identity{name, sqs.NewFromConfig(cfg), wire}
}

// role assumes one of the four homologation roles through STS, exercising the
// real temporary-credential path instead of long-lived access keys.
func role(t *testing.T, e environment, base string) identity {
	t.Helper()
	name := fmt.Sprintf("%s%s", strings.TrimSuffix(e.prefix, "-"), "-"+base)
	provider := stscreds.NewAssumeRoleProvider(sts.NewFromConfig(e.base),
		fmt.Sprintf("arn:aws:iam::%s:role/%s", e.account, name),
		func(o *stscreds.AssumeRoleOptions) { o.RoleSessionName = "homolog-probe-" + uuid.NewString()[:12] })
	if _, err := provider.Retrieve(t.Context()); err != nil {
		t.Fatalf("cannot assume role %s (run scripts/aws-homolog-apply.sh first): %v", name, err)
	}
	return newClient(t, name, e, provider)
}

var denyCodes = map[string]bool{
	"AccessDenied": true, "AccessDeniedException": true,
	"InvalidClientTokenId": true, "UnrecognizedClientException": true,
	"SignatureDoesNotMatch": true, "InvalidSignatureException": true,
	"MissingAuthenticationTokenException": true, "ExpiredToken": true,
}

func record(t *testing.T, who identity, action, resource string, allow bool, err error) {
	t.Helper()
	code := ""
	var api smithy.APIError
	if errors.As(err, &api) {
		code = api.ErrorCode()
	}
	status := int(who.wire.status.Load())
	// A transport failure or unrelated error never counts as an IAM denial.
	pass := (allow && err == nil && status == 200) || (!allow && err != nil && denyCodes[code])
	t.Logf(`{"principal":%q,"action":%q,"resource":%q,"expectedAllow":%v,"httpStatus":%d,"errorCode":%q,"pass":%v}`,
		who.name, action, resource, allow, status, code, pass)
	if !pass {
		t.Errorf("broker gate failed: %s %s expectedAllow=%v HTTP=%d code=%s", who.name, action, allow, status, code)
	}
}

func names(e environment) sqsadapter.QueueNames {
	return (sqsadapter.Config{QueuePrefix: e.prefix}).QueueNames()
}

func send(t *testing.T, who identity, queue, body, group, dedup string) (*sqs.SendMessageOutput, error) {
	t.Helper()
	who.wire.status.Store(0)
	return who.client.SendMessage(t.Context(), &sqs.SendMessageInput{QueueUrl: aws.String(queue), MessageBody: aws.String(body), MessageGroupId: aws.String(group), MessageDeduplicationId: aws.String(dedup)})
}

func receive(t *testing.T, who identity, queue string, visibility int32) (*sqs.ReceiveMessageOutput, error) {
	t.Helper()
	who.wire.status.Store(0)
	return who.client.ReceiveMessage(t.Context(), &sqs.ReceiveMessageInput{QueueUrl: aws.String(queue), MaxNumberOfMessages: 1, VisibilityTimeout: visibility, MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameAll}})
}

// TestAWSSQSIAM proves the broker itself enforces the four least-privilege
// roles on real AWS SQS: positive paths, cross-queue denials, anonymous and
// forged-credential rejections (A01).
func TestAWSSQSIAM(t *testing.T) {
	e := env(t)
	n := names(e)
	admin := role(t, e, "jungle-queue-admin")
	for _, name := range []string{n.DLQ, n.Wager, n.Events} {
		_, err := admin.client.CreateQueue(t.Context(), &sqs.CreateQueueInput{QueueName: aws.String(name), Attributes: map[string]string{"FifoQueue": "true"}})
		record(t, admin, "CreateQueue", name, true, err)
		if err != nil {
			t.FailNow()
		}
	}
	q, err := sqsadapter.ProvisionNamed(t.Context(), admin.client, n)
	if err != nil {
		t.Fatal(err)
	}
	_, err = admin.client.SetQueueAttributes(t.Context(), &sqs.SetQueueAttributesInput{QueueUrl: aws.String(q.WagerQueueURL), Attributes: map[string]string{"VisibilityTimeout": "30"}})
	record(t, admin, "SetQueueAttributes", n.Wager, true, err)

	producer := role(t, e, "jungle-wager-producer")
	consumer := role(t, e, "jungle-wager-consumer")
	publisher := role(t, e, "jungle-event-publisher")

	_, err = send(t, producer, q.WagerQueueURL, "iam-fixture", "iam", uuid.NewString())
	record(t, producer, "SendMessage", n.Wager, true, err)
	_, err = receive(t, producer, q.WagerQueueURL, 0)
	record(t, producer, "ReceiveMessage", n.Wager, false, err)
	got, err := receive(t, consumer, q.WagerQueueURL, 30)
	record(t, consumer, "ReceiveMessage", n.Wager, true, err)
	if err != nil || len(got.Messages) != 1 {
		t.Fatal("consumer did not receive fixture")
	}
	receipt := got.Messages[0].ReceiptHandle
	_, err = producer.client.DeleteMessage(t.Context(), &sqs.DeleteMessageInput{QueueUrl: aws.String(q.WagerQueueURL), ReceiptHandle: receipt})
	record(t, producer, "DeleteMessage", n.Wager, false, err)
	_, err = consumer.client.DeleteMessage(t.Context(), &sqs.DeleteMessageInput{QueueUrl: aws.String(q.WagerQueueURL), ReceiptHandle: receipt})
	record(t, consumer, "DeleteMessage", n.Wager, true, err)
	_, err = send(t, publisher, q.EventsQueueURL, "event-fixture", "events", uuid.NewString())
	record(t, publisher, "SendMessage", n.Events, true, err)
	_, err = send(t, publisher, q.WagerQueueURL, "forbidden", "iam", uuid.NewString())
	record(t, publisher, "SendMessage", n.Wager, false, err)
	_, err = receive(t, consumer, q.EventsQueueURL, 0)
	record(t, consumer, "ReceiveMessage", n.Events, false, err)
	_, err = consumer.client.DeleteQueue(t.Context(), &sqs.DeleteQueueInput{QueueUrl: aws.String(q.WagerQueueURL)})
	record(t, consumer, "DeleteQueue", n.Wager, false, err)

	// Anonymous, unknown key and tampered producer secret must be rejected.
	anonymous := newClient(t, "anonymous", e, aws.AnonymousCredentials{})
	_, err = send(t, anonymous, q.WagerQueueURL, "forbidden-anonymous", "iam", uuid.NewString())
	record(t, anonymous, "SendMessage", n.Wager, false, err)
	unknown := newClient(t, "unknown-key", e, aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: "AKIAHOMOLOGUNKNOWN", SecretAccessKey: "wrong"}, nil
	}))
	_, err = receive(t, unknown, q.WagerQueueURL, 0)
	record(t, unknown, "ReceiveMessage", n.Wager, false, err)
	// Tampered producer secret must be rejected by the broker signature check.
	producerRoleName := strings.TrimSuffix(e.prefix, "-") + "-jungle-wager-producer"
	producerProvider := stscreds.NewAssumeRoleProvider(sts.NewFromConfig(e.base),
		fmt.Sprintf("arn:aws:iam::%s:role/%s", e.account, producerRoleName),
		func(o *stscreds.AssumeRoleOptions) {
			o.RoleSessionName = "homolog-probe-tampered-" + uuid.NewString()[:8]
		})
	real, err := producerProvider.Retrieve(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	tampered := newClient(t, "producer-wrong-secret", e, aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: real.AccessKeyID, SecretAccessKey: "tampered" + real.SecretAccessKey, SessionToken: real.SessionToken}, nil
	}))
	_, err = send(t, tampered, q.WagerQueueURL, "forbidden-tampered", "iam", uuid.NewString())
	record(t, tampered, "SendMessage", n.Wager, false, err)
}

// TestAWSSQSDurability proves broker-level durability semantics on real AWS:
// crash-before-delete redelivery with identity preserved, visibility honored,
// FIFO dedup window and DLQ redrive (A02 at the broker).
func TestAWSSQSDurability(t *testing.T) {
	e := env(t)
	n := names(e)
	admin := role(t, e, "jungle-queue-admin")
	q, err := sqsadapter.ProvisionNamed(t.Context(), admin.client, n)
	if err != nil {
		t.Fatal(err)
	}
	producer := role(t, e, "jungle-wager-producer")
	consumer := role(t, e, "jungle-wager-consumer")

	dedup := uuid.NewString()
	sent, err := send(t, producer, q.WagerQueueURL, "durable-body", "durable-group", dedup)
	if err != nil {
		t.Fatal(err)
	}
	// Crash before DeleteMessage: receive and keep the message invisible.
	first, err := receive(t, consumer, q.WagerQueueURL, 5)
	if err != nil || len(first.Messages) != 1 {
		t.Fatalf("first receive: %v", err)
	}
	if aws.ToString(first.Messages[0].MessageId) != aws.ToString(sent.MessageId) {
		t.Fatal("identity changed on first delivery")
	}
	// Before the visibility timeout the message must NOT redeliver.
	if out, err := receive(t, consumer, q.WagerQueueURL, 1); err != nil || len(out.Messages) != 0 {
		t.Fatalf("visibility timeout violated: message redelivered early: %v", err)
	}
	time.Sleep(6 * time.Second)
	second, err := receive(t, consumer, q.WagerQueueURL, 5)
	if err != nil || len(second.Messages) != 1 {
		t.Fatalf("redelivery after visibility timeout failed: %v", err)
	}
	m := second.Messages[0]
	if aws.ToString(m.MessageId) != aws.ToString(sent.MessageId) || aws.ToString(m.Body) != "durable-body" ||
		m.Attributes["MessageGroupId"] != "durable-group" || m.Attributes["MessageDeduplicationId"] != dedup ||
		m.Attributes["ApproximateReceiveCount"] != "2" {
		t.Fatalf("redelivery changed identity/body/attributes: %v", m)
	}
	t.Logf("redelivered id=%s receives=%s identity preserved", aws.ToString(m.MessageId), m.Attributes["ApproximateReceiveCount"])
	if _, err := consumer.client.DeleteMessage(t.Context(), &sqs.DeleteMessageInput{QueueUrl: aws.String(q.WagerQueueURL), ReceiptHandle: m.ReceiptHandle}); err != nil {
		t.Fatal(err)
	}
	// FIFO dedup window: resend with the same dedup id; no second delivery.
	duplicate, err := send(t, producer, q.WagerQueueURL, "durable-body", "durable-group", dedup)
	if err != nil || aws.ToString(duplicate.MessageId) != aws.ToString(sent.MessageId) {
		t.Fatalf("deduplication identity mismatch: %v", err)
	}
	if out, err := receive(t, consumer, q.WagerQueueURL, 1); err != nil || len(out.Messages) != 0 {
		t.Fatalf("deduplicated message was delivered again: %v", err)
	}

	// DLQ redrive: 3 receives without delete, then the source stops delivering
	// and the operator identity (base credentials) finds the message in the DLQ.
	poisonDedup := uuid.NewString()
	poison, err := send(t, producer, q.WagerQueueURL, "poison-homolog", "dlq-group", poisonDedup)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if i > 0 {
			time.Sleep(1300 * time.Millisecond)
		}
		out, err := receive(t, consumer, q.WagerQueueURL, 1)
		if err != nil || len(out.Messages) != 1 || aws.ToString(out.Messages[0].MessageId) != aws.ToString(poison.MessageId) {
			t.Fatalf("delivery %d: %v", i+1, err)
		}
	}
	time.Sleep(1300 * time.Millisecond)
	if out, err := receive(t, consumer, q.WagerQueueURL, 1); err != nil || len(out.Messages) != 0 {
		t.Fatalf("source kept delivering after maxReceiveCount: %v", err)
	}
	operator := newClient(t, "operator-base", e, e.base.Credentials)
	dlq, err := receive(t, operator, q.DLQQueueURL, 1)
	if err != nil || len(dlq.Messages) != 1 {
		t.Fatalf("DLQ did not receive the poison message: %v", err)
	}
	if dm := dlq.Messages[0]; aws.ToString(dm.MessageId) != aws.ToString(poison.MessageId) || aws.ToString(dm.Body) != "poison-homolog" ||
		dm.Attributes["MessageGroupId"] != "dlq-group" || dm.Attributes["MessageDeduplicationId"] != poisonDedup {
		t.Fatalf("DLQ changed identity/body/attributes: %v", dm)
	}
	t.Log("DLQ redrive preserved message identity on managed AWS")
}

// TestAWSSQSAppRecovery proves the application-level A02 guarantees on real
// AWS: crash before DeleteMessage resolves as one debit (inbox replay), and
// the outbox window (send accepted, ack interrupted) republishes the same
// eventId and payload bytes.
func TestAWSSQSAppRecovery(t *testing.T) {
	e := env(t)
	pool := homologPostgres(t)
	defer pool.Close()
	admin := role(t, e, "jungle-queue-admin")
	q, err := sqsadapter.ProvisionNamed(t.Context(), admin.client, names(e))
	if err != nil {
		t.Fatal(err)
	}
	producer := role(t, e, "jungle-wager-producer")
	consumerIdentity := role(t, e, "jungle-wager-consumer")
	operator := newClient(t, "operator-base", e, e.base.Credentials)

	uow := func() *postgres.UnitOfWork { return postgres.NewUnitOfWork(pool) }
	process := command.ProcessWagerDeps{
		UnitOfWork: uow(), Wallets: postgres.NewWalletRepository(), Wagers: postgres.NewWagerTransactionRepository(),
		WagerLookups: postgres.NewWagerTransactionReader(pool), Ledger: postgres.NewLedgerRepository(),
		Outbox: postgres.NewOutboxRepository(), Now: func() time.Time { return time.Now().UTC() }, NewID: uuid.New,
	}
	create := command.CreateWalletDeps{UnitOfWork: uow(), Wallets: postgres.NewWalletRepository(), Wagers: postgres.NewWagerTransactionRepository(),
		Ledger: postgres.NewLedgerRepository(), Outbox: postgres.NewOutboxRepository(), Now: func() time.Time { return time.Now().UTC() }, NewID: uuid.New}
	consume := command.ConsumeWagerDeps{UnitOfWork: uow(), Process: process, Inbox: postgres.NewInboxRepository(), Now: func() time.Time { return time.Now().UTC() }}
	consumer := &sqsadapter.Consumer{Consume: consume.ConsumeWagerMessage, Client: consumerIdentity.client, WagerQueueURL: q.WagerQueueURL,
		DLQQueueURL: q.DLQQueueURL, ConsumerName: "homolog-consumer", PollWait: 1, ProviderKeys: sqsadapter.ProviderKeys{"provider-a": providerKey}}

	balance := func(w uuid.UUID) string {
		var b int64
		if err := pool.QueryRow(t.Context(), "SELECT balance_cents FROM wallets WHERE id=$1", w).Scan(&b); err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("%d.%02d", b/100, b%100)
	}
	player := uuid.New()
	created, err := create.CreateWallet(t.Context(), command.CreateWalletInput{PlayerID: player, InitialBalance: mustMoney(t, "100.00"), CorrelationID: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	wallet := created.WalletID

	runCtx, stop := context.WithCancel(t.Context())
	defer stop()
	go consumer.Run(runCtx)

	sendWager := func(externalID, amount string) {
		t.Helper()
		body := fmt.Sprintf(`{"messageId":%q,"type":"WagerTransactionRequested","occurredAt":%q,"data":{"providerId":"provider-a","externalTransactionId":%q,"idempotencyKey":%q,"playerId":%q,"walletId":%q,"roundId":"homolog","gameId":"fortune-chimp","kind":"BET","money":{"amount":%q,"currency":"BRL"}}}`,
			"homolog-"+externalID, time.Now().UTC().Format(time.RFC3339), externalID, externalID, player, wallet, amount)
		_, err := producer.client.SendMessage(t.Context(), &sqs.SendMessageInput{QueueUrl: aws.String(q.WagerQueueURL), MessageBody: aws.String(body),
			MessageGroupId: aws.String(wallet.String()), MessageDeduplicationId: aws.String(uuid.NewString()),
			MessageAttributes: sqsadapter.SignAttributes("provider-a", providerKey, []byte(body))})
		if err != nil {
			t.Fatal(err)
		}
	}

	// 1) Normal consumption debits once.
	sendWager(uuid.NewString()[:12], "25.00")
	waitFor(t, 30*time.Second, func() bool { return balance(wallet) == "75.00" })

	// 2) Crash before DeleteMessage: the probe receives the wager without
	// deleting (visibility 5s); the consumer must receive the SAME broker
	// message after the timeout and debit it exactly once via the inbox.
	external := uuid.NewString()[:12]
	sendWager(external, "30.00")
	inflight, err := receive(t, consumerIdentity, q.WagerQueueURL, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(inflight.Messages) == 0 {
		t.Fatal("probe did not capture the in-flight wager")
	}
	if len(inflight.Messages) > 1 || !strings.Contains(aws.ToString(inflight.Messages[0].Body), external) {
		t.Fatalf("probe captured unexpected message: %v", inflight.Messages)
	}
	time.Sleep(6 * time.Second)
	waitFor(t, 30*time.Second, func() bool { return balance(wallet) == "45.00" })
	var inboxCount int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM inbox_messages WHERE message_id=$1 AND completed_at IS NOT NULL", "homolog-"+external).Scan(&inboxCount); err != nil {
		t.Fatal(err)
	}
	if inboxCount != 1 {
		t.Fatalf("expected one completed inbox entry, got %d", inboxCount)
	}
	var ledger int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM ledger_entries WHERE wallet_id=$1", wallet).Scan(&ledger); err != nil {
		t.Fatal(err)
	}
	if ledger != 3 { // opening + BET 25.00 + BET 30.00
		t.Fatalf("expected 3 ledger entries, got %d", ledger)
	}

	// 3) Outbox window: publish accepted, ack interrupted — republication must
	// carry the same eventId and payload bytes (at-least-once, never lost).
	// 3) Outbox window: publish accepted, ack interrupted — republication must
	// carry the same eventId and payload bytes (at-least-once, never lost).
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	publisher := &sqsadapter.EventPublisher{Client: role(t, e, "jungle-event-publisher").client, QueueURL: q.EventsQueueURL, Logger: discard}
	outbox := postgres.NewOutboxDeliveryRepository(pool)
	metrics := observability.NewOutboxMetrics()
	deps := command.PublishOutboxDeps{Outbox: outbox, Publisher: publisher, Metrics: metrics, Logger: discard, PostSendPause: 3 * time.Second}
	worker := &workers.OutboxWorker{PublishNext: deps.PublishNext, Interval: 300 * time.Millisecond, Logger: discard}
	workerCtx, stopWorker := context.WithCancel(t.Context())
	go worker.Run(workerCtx)

	if _, err := process.ProcessWager(t.Context(), command.ProcessWagerInput{ProviderID: "provider-a", ExternalTransactionID: "homolog-outbox-" + uuid.NewString()[:8],
		IdempotencyKey: uuid.NewString(), PlayerID: player, WalletID: wallet, RoundID: "homolog", GameID: "fortune-chimp",
		Kind: wagering.Win, Money: mustMoney(t, "10.00"), CorrelationID: uuid.New()}); err != nil {
		t.Fatal(err)
	}
	firstEvent, err := receive(t, operator, q.EventsQueueURL, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstEvent.Messages) != 1 {
		t.Fatalf("expected one event on the events queue, got %d", len(firstEvent.Messages))
	}
	eventBody := aws.ToString(firstEvent.Messages[0].Body)
	stopWorker() // interrupt during PostSendPause: send accepted, ack not committed
	time.Sleep(500 * time.Millisecond)

	// Restart the publisher without the pause; it must republish the same row.
	deps.PostSendPause = 0
	worker2 := &workers.OutboxWorker{PublishNext: deps.PublishNext, Interval: 300 * time.Millisecond}
	workerCtx2, stopWorker2 := context.WithCancel(t.Context())
	defer stopWorker2()
	go worker2.Run(workerCtx2)
	secondEvent, err := receive(t, operator, q.EventsQueueURL, 5)
	if err != nil || len(secondEvent.Messages) != 1 {
		t.Fatalf("republication not observed: %v", err)
	}
	if aws.ToString(secondEvent.Messages[0].Body) != eventBody {
		t.Fatal("republication changed the event payload bytes")
	}
	t.Log("outbox republication preserved eventId and payload after ack interruption")
}

func mustMoney(t *testing.T, amount string) money.Money {
	t.Helper()
	m, err := money.ParseDecimal(amount, money.BRL)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func waitFor(t *testing.T, timeout time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}

// homologPostgres connects to the homologation PostgreSQL (migrated by the
// compose.homolog.yaml job). Skips without an explicit TEST_DATABASE_URL.
func homologPostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set: the homologation PostgreSQL is required")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connect homologation PostgreSQL: %v", err)
	}
	if err := pool.Ping(t.Context()); err != nil {
		pool.Close()
		t.Fatalf("ping homologation PostgreSQL: %v", err)
	}
	return pool
}
