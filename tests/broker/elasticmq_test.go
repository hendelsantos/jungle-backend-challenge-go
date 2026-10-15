//go:build elasticmqpoc

// ElasticMQ 1.7.1 acceptance probes (messages-storage enabled, H2 event log):
// SDK Go v2 compatibility, broker authorization behavior (A01) and message
// durability across graceful restart and SIGKILL (A02). Deliberately FAIL when
// the broker violates a gate — a failure is the rejection evidence. Disposable
// project only; never the application DB.
package broker_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/aws/smithy-go"
	"github.com/google/uuid"
	sqsadapter "github.com/hendelsantos/jungle-backend-challenge-go/internal/adapters/sqs"
)

type eqTransport struct{ status atomic.Int32 }

func (r *eqTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "http" || req.URL.Hostname() != "127.0.0.1" {
		return nil, fmt.Errorf("POC permits only loopback HTTP")
	}
	res, err := http.DefaultTransport.RoundTrip(req)
	if res != nil {
		r.status.Store(int32(res.StatusCode))
	}
	return res, err
}

type eqIdentity struct {
	name   string
	client *sqs.Client
	wire   *eqTransport
}

func eqEndpoint(t *testing.T) string {
	t.Helper()
	if os.Getenv("ELASTICMQ_POC_DISPOSABLE") != "1" {
		t.Fatal("ELASTICMQ_POC_DISPOSABLE=1 required; this probe restarts its dedicated broker")
	}
	u, err := url.Parse(os.Getenv("ELASTICMQ_POC_ENDPOINT"))
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" {
		t.Fatal("ELASTICMQ_POC_ENDPOINT must be an explicit loopback HTTP endpoint")
	}
	return strings.TrimRight(u.String(), "/")
}

func eqClient(t *testing.T, name string, creds aws.CredentialsProvider) eqIdentity {
	t.Helper()
	wire := &eqTransport{}
	c := sqs.NewFromConfig(aws.Config{Region: "us-east-1", Credentials: creds, HTTPClient: &http.Client{Transport: wire, Timeout: 10 * time.Second}, RetryMaxAttempts: 1}, func(o *sqs.Options) { o.BaseEndpoint = aws.String(eqEndpoint(t)) })
	return eqIdentity{name, c, wire}
}

func eqRecord(t *testing.T, who eqIdentity, action string, allow bool, err error) {
	t.Helper()
	code := ""
	var api smithy.APIError
	if errors.As(err, &api) {
		code = api.ErrorCode()
	}
	status := int(who.wire.status.Load())
	denied := err != nil && (code == "AccessDenied" || code == "AccessDeniedException" || code == "InvalidClientTokenId" || code == "UnrecognizedClientException" || code == "SignatureDoesNotMatch" || code == "InvalidSignatureException" || code == "MissingAuthenticationTokenException")
	pass := (allow && err == nil && status == 200) || (!allow && denied)
	b, _ := json.Marshal(map[string]any{"principal": who.name, "action": action, "expectedAllow": allow, "httpStatus": status, "errorCode": code, "pass": pass})
	t.Log(string(b))
	if !pass {
		t.Errorf("broker gate failed: %s %s expectedAllow=%v HTTP=%d code=%s", who.name, action, allow, status, code)
	}
}

func eqNames() sqsadapter.QueueNames {
	return (sqsadapter.Config{QueuePrefix: "eqpoc-" + uuid.NewString()[:8] + "-"}).QueueNames()
}

func eqSend(t *testing.T, who eqIdentity, queue, body, group, dedup string) (*sqs.SendMessageOutput, error) {
	t.Helper()
	who.wire.status.Store(0)
	return who.client.SendMessage(t.Context(), &sqs.SendMessageInput{QueueUrl: aws.String(queue), MessageBody: aws.String(body), MessageGroupId: aws.String(group), MessageDeduplicationId: aws.String(dedup)})
}

func eqReceive(t *testing.T, who eqIdentity, queue string, visibility int32) (*sqs.ReceiveMessageOutput, error) {
	t.Helper()
	who.wire.status.Store(0)
	return who.client.ReceiveMessage(t.Context(), &sqs.ReceiveMessageInput{QueueUrl: aws.String(queue), MaxNumberOfMessages: 1, VisibilityTimeout: visibility, MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameAll}})
}

func eqRestart(t *testing.T, abrupt bool) {
	t.Helper()
	project := os.Getenv("ELASTICMQ_POC_PROJECT")
	if !regexp.MustCompile(`^jungle-elasticmq-poc-[a-z0-9-]+$`).MatchString(project) {
		t.Fatal("explicit jungle-elasticmq-poc-* project required")
	}
	run := func(args ...string) {
		ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
		defer cancel()
		base := []string{"compose", "-p", project, "-f", "../../compose.elasticmq.yaml"}
		if out, err := exec.CommandContext(ctx, "docker", append(base, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("broker lifecycle %v: %v %s", args, err, out)
		}
	}
	if abrupt {
		run("kill", "-s", "KILL", "elasticmq")
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "docker", "wait", project+"-elasticmq-1").CombinedOutput()
		if err != nil || strings.TrimSpace(string(out)) != "137" {
			t.Fatalf("SIGKILL exit precondition: %v %s", err, out)
		}
		t.Log("broker exited with code 137 before restart")
		run("start", "elasticmq")
	} else {
		run("restart", "-t", "30", "elasticmq")
	}
	client := &http.Client{Timeout: 2 * time.Second}
	for i := 0; i < 90; i++ {
		res, err := client.Get(eqEndpoint(t) + "/health")
		if err == nil {
			res.Body.Close()
			if res.StatusCode == 200 {
				return
			}
		}
		select {
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		case <-time.After(time.Second):
		}
	}
	t.Fatal("broker did not recover")
}

// eqProvision creates the FIFO queues with raw SDK calls. It does NOT use
// sqsadapter.ProvisionNamed on purpose: ElasticMQ 1.7.1 rejects
// ContentBasedDeduplication via SetQueueAttributes (InvalidAttributeName),
// while real AWS accepts it — a recorded compatibility deviation. The
// adapter's reconcile contract stays unchanged; this probe measures the broker.
func eqProvision(t *testing.T, admin eqIdentity, n sqsadapter.QueueNames) sqsadapter.Provisioned {
	t.Helper()
	var urls struct{ wager, dlq, events string }
	create := func(name string, redrive bool) string {
		t.Helper()
		out, err := admin.client.CreateQueue(t.Context(), &sqs.CreateQueueInput{QueueName: aws.String(name), Attributes: map[string]string{"FifoQueue": "true", "ContentBasedDeduplication": "false"}})
		if err != nil {
			t.Fatalf("CreateQueue %s: %v", name, err)
		}
		url := aws.ToString(out.QueueUrl)
		if redrive {
			dlqARN := "arn:aws:sqs:us-east-1:000000000000:" + n.DLQ
			policy := fmt.Sprintf(`{"deadLetterTargetArn":%q,"maxReceiveCount":"3"}`, dlqARN)
			if _, err := admin.client.SetQueueAttributes(t.Context(), &sqs.SetQueueAttributesInput{QueueUrl: aws.String(url), Attributes: map[string]string{"RedrivePolicy": policy, "VisibilityTimeout": "30"}}); err != nil {
				t.Fatalf("RedrivePolicy %s: %v", name, err)
			}
		}
		return url
	}
	urls.dlq = create(n.DLQ, false)
	urls.wager = create(n.Wager, true)
	urls.events = create(n.Events, false)
	return sqsadapter.Provisioned{WagerQueueURL: urls.wager, DLQQueueURL: urls.dlq, EventsQueueURL: urls.events}
}

// TestElasticMQAuthz records whether the broker controls access at all. If it
// accepts anonymous or forged credentials, A01 stays unresolved on ElasticMQ.
func TestElasticMQAuthz(t *testing.T) {
	admin := eqClient(t, "static-creds", aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: "poc-key", SecretAccessKey: "poc-secret"}, nil
	}))
	n := eqNames()
	q := eqProvision(t, admin, n)
	_, err := eqSend(t, admin, q.WagerQueueURL, "authorized-fixture", "iam", uuid.NewString())
	eqRecord(t, admin, "SendMessage", true, err)
	for _, who := range []eqIdentity{
		eqClient(t, "anonymous", aws.AnonymousCredentials{}),
		eqClient(t, "unknown-key", aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "poc-unknown", SecretAccessKey: "wrong"}, nil
		})),
		eqClient(t, "wrong-secret", aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "poc-key", SecretAccessKey: "wrong-secret"}, nil
		})),
	} {
		_, err := eqSend(t, who, q.WagerQueueURL, "forbidden-"+who.name, "iam", uuid.NewString())
		eqRecord(t, who, "SendMessage", false, err)
	}
}

// TestElasticMQDurability mirrors the MiniStack durability gate: accepted
// messages must survive a graceful restart AND an abrupt SIGKILL with identity
// (id/body/group/dedup) and receive counts intact.
func TestElasticMQDurability(t *testing.T) {
	for _, abrupt := range []bool{false, true} {
		t.Run(fmt.Sprintf("sigkill=%v", abrupt), func(t *testing.T) {
			admin := eqClient(t, "static-creds", aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
				return aws.Credentials{AccessKeyID: "poc-key", SecretAccessKey: "poc-secret"}, nil
			}))
			n := eqNames()
			q := eqProvision(t, admin, n)
			ids := make([]string, 3)
			bodies := []string{"visible-A", "visible-B", "inflight-C"}
			groups := []string{"ordered", "ordered", "inflight"}
			dedup := []string{uuid.NewString(), uuid.NewString(), uuid.NewString()}
			for i := range bodies {
				out, err := eqSend(t, admin, q.EventsQueueURL, bodies[i], groups[i], dedup[i])
				if err != nil {
					t.Fatal(err)
				}
				ids[i] = aws.ToString(out.MessageId)
				t.Logf("accepted id=%s body=%s group=%s dedup=%s HTTP=%d", ids[i], bodies[i], groups[i], dedup[i], admin.wire.status.Load())
			}
			inflight, err := eqReceive(t, admin, q.EventsQueueURL, 30)
			if err != nil || len(inflight.Messages) != 1 || aws.ToString(inflight.Messages[0].MessageId) != ids[0] {
				t.Fatalf("inflight precondition: %v", err)
			}
			eqRestart(t, abrupt)
			resolved, err := admin.client.GetQueueUrl(t.Context(), &sqs.GetQueueUrlInput{QueueName: aws.String(n.Events)})
			if err != nil || aws.ToString(resolved.QueueUrl) != q.EventsQueueURL {
				t.Fatalf("queue lost after restart: %v", err)
			}
			seen := map[string]bool{}
			deadline := time.Now().Add(45 * time.Second)
			for time.Now().Before(deadline) && len(seen) < len(ids) {
				out, err := eqReceive(t, admin, q.EventsQueueURL, 2)
				if err != nil {
					t.Fatal(err)
				}
				for _, m := range out.Messages {
					id := aws.ToString(m.MessageId)
					index := -1
					for i, want := range ids {
						if id == want {
							index = i
						}
					}
					if index < 0 {
						t.Fatalf("unexpected recovered id %s", id)
					}
					if seen[id] || aws.ToString(m.Body) != bodies[index] || m.Attributes["MessageGroupId"] != groups[index] || m.Attributes["MessageDeduplicationId"] != dedup[index] {
						t.Fatalf("recovered duplicate or changed message %s", id)
					}
					seen[id] = true
					t.Logf("recovered id=%s body=%s receives=%s", id, aws.ToString(m.Body), m.Attributes["ApproximateReceiveCount"])
					if _, err := admin.client.DeleteMessage(t.Context(), &sqs.DeleteMessageInput{QueueUrl: aws.String(q.EventsQueueURL), ReceiptHandle: m.ReceiptHandle}); err != nil {
						t.Fatal(err)
					}
				}
				if len(seen) < len(ids) {
					time.Sleep(250 * time.Millisecond)
				}
			}
			if len(seen) != len(ids) {
				t.Errorf("accepted=%d recovered=%d lost=%d (queue exists; no reprovision after restart)", len(ids), len(seen), len(ids)-len(seen))
			}
		})
	}
}

// TestElasticMQDLQ verifies redrive to the DLQ and that the DLQ content
// survives a graceful restart with identity preserved.
func TestElasticMQDLQ(t *testing.T) {
	admin := eqClient(t, "static-creds", aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: "poc-key", SecretAccessKey: "poc-secret"}, nil
	}))
	q := eqProvision(t, admin, eqNames())
	dedup := uuid.NewString()
	sent, err := eqSend(t, admin, q.WagerQueueURL, "poison-message", "dlq-group", dedup)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if i > 0 {
			time.Sleep(1500 * time.Millisecond)
		}
		out, err := eqReceive(t, admin, q.WagerQueueURL, 1)
		if err != nil || len(out.Messages) != 1 || aws.ToString(out.Messages[0].MessageId) != aws.ToString(sent.MessageId) {
			t.Fatalf("delivery %d: %v", i+1, err)
		}
	}
	time.Sleep(1500 * time.Millisecond)
	if out, err := eqReceive(t, admin, q.WagerQueueURL, 1); err != nil || len(out.Messages) != 0 {
		t.Fatalf("redrive failed: %v", err)
	}
	eqRestart(t, false)
	dlq, err := eqReceive(t, admin, q.DLQQueueURL, 30)
	if err != nil || len(dlq.Messages) != 1 {
		t.Fatalf("DLQ empty after restart: %v", err)
	}
	if m := dlq.Messages[0]; aws.ToString(m.Body) != "poison-message" || aws.ToString(m.MessageId) != aws.ToString(sent.MessageId) || m.Attributes["MessageGroupId"] != "dlq-group" || m.Attributes["MessageDeduplicationId"] != dedup {
		t.Fatalf("DLQ changed identity/body/attributes: got id=%s body=%q group=%s dedup=%s, want id=%s body=%q",
			aws.ToString(m.MessageId), aws.ToString(m.Body), m.Attributes["MessageGroupId"], m.Attributes["MessageDeduplicationId"], aws.ToString(sent.MessageId), "poison-message")
	}
	t.Logf("DLQ recovered id=%s with identity intact", aws.ToString(sent.MessageId))
}
