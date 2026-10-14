//go:build ministackpoc

// These acceptance probes deliberately FAIL when the broker violates a gate.
// They use a real, explicitly disposable MiniStack, never the application DB.
package broker_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
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
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/aws/smithy-go"
	"github.com/google/uuid"
	sqsadapter "github.com/hendelsantos/jungle-backend-challenge-go/internal/adapters/sqs"
)

const region = "us-east-1"

// Observe the actual broker HTTP status and forbid any AWS/cloud fallback.
type transport struct{ status atomic.Int32 }

func (r *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "http" || req.URL.Hostname() != "127.0.0.1" {
		return nil, fmt.Errorf("POC permits only loopback HTTP")
	}
	res, err := http.DefaultTransport.RoundTrip(req)
	if res != nil {
		r.status.Store(int32(res.StatusCode))
	}
	return res, err
}

type identity struct {
	name   string
	key    aws.Credentials
	client *sqs.Client
	wire   *transport
}

func endpoint(t *testing.T) string {
	t.Helper()
	if os.Getenv("MINISTACK_POC_DISPOSABLE") != "1" {
		t.Fatal("MINISTACK_POC_DISPOSABLE=1 required; this probe restarts its dedicated broker")
	}
	u, err := url.Parse(os.Getenv("MINISTACK_POC_ENDPOINT"))
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		t.Fatal("MINISTACK_POC_ENDPOINT must be an explicit loopback HTTP endpoint")
	}
	return strings.TrimRight(u.String(), "/")
}

func newIdentity(t *testing.T, name string, key aws.Credentials) identity {
	t.Helper()
	wire := &transport{}
	var provider aws.CredentialsProvider = aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) { return key, nil })
	if name == "anonymous" {
		provider = aws.AnonymousCredentials{}
	}
	c := sqs.NewFromConfig(aws.Config{Region: region, Credentials: provider, HTTPClient: &http.Client{Transport: wire, Timeout: 10 * time.Second}, RetryMaxAttempts: 1}, func(o *sqs.Options) { o.BaseEndpoint = aws.String(endpoint(t)) })
	return identity{name, key, c, wire}
}

func root(t *testing.T) identity {
	return newIdentity(t, "bootstrap-root", aws.Credentials{AccessKeyID: "jungle-poc-bootstrap", SecretAccessKey: "local-only-bootstrap"})
}

func record(t *testing.T, who identity, action, resource string, allow bool, err error) {
	t.Helper()
	code := ""
	var api smithy.APIError
	if errors.As(err, &api) {
		code = api.ErrorCode()
	}
	status := int(who.wire.status.Load())
	// A transport failure or unrelated error never counts as IAM denial.
	denied := status == 403 && (code == "AccessDenied" || code == "AccessDeniedException" || code == "InvalidClientTokenId" || code == "UnrecognizedClientException" || code == "SignatureDoesNotMatch" || code == "InvalidSignatureException")
	pass := (allow && err == nil && status == 200) || (!allow && denied)
	b, _ := json.Marshal(map[string]any{"principal": who.name, "action": action, "resource": resource, "expectedAllow": allow, "httpStatus": status, "errorCode": code, "pass": pass})
	t.Log(string(b))
	if !pass {
		t.Errorf("broker gate failed: %s %s expectedAllow=%v HTTP=%d code=%s", who.name, action, allow, status, code)
	}
}

// IAM is AWS Query, signed using the project's existing AWS SDK v2 signer.
// No IAM dependency or credentials file is added to the application.
func iam(t *testing.T, admin identity, values url.Values) []byte {
	t.Helper()
	values.Set("Version", "2010-05-08")
	body := values.Encode()
	req, err := http.NewRequestWithContext(t.Context(), "POST", endpoint(t)+"/", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	hash := sha256.Sum256([]byte(body))
	if err := v4.NewSigner().SignHTTP(t.Context(), admin.key, req, hex.EncodeToString(hash[:]), "iam", region, time.Now()); err != nil {
		t.Fatal(err)
	}
	res, err := (&http.Client{Transport: admin.wire, Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("IAM setup %s: HTTP=%d readErr=%v", values.Get("Action"), res.StatusCode, err)
	}
	return data
}

func user(t *testing.T, admin identity, name string, policy any) identity {
	t.Helper()
	name += "-" + uuid.NewString()[:8]
	iam(t, admin, url.Values{"Action": {"CreateUser"}, "UserName": {name}})
	document, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	iam(t, admin, url.Values{"Action": {"PutUserPolicy"}, "UserName": {name}, "PolicyName": {"poc"}, "PolicyDocument": {string(document)}})
	data := iam(t, admin, url.Values{"Action": {"CreateAccessKey"}, "UserName": {name}})
	var result struct {
		Key struct {
			ID     string `xml:"AccessKeyId"`
			Secret string `xml:"SecretAccessKey"`
		} `xml:"CreateAccessKeyResult>AccessKey"`
	}
	if err := xml.Unmarshal(data, &result); err != nil || result.Key.ID == "" || result.Key.Secret == "" {
		t.Fatal("IAM did not return valid credentials")
	}
	return newIdentity(t, name, aws.Credentials{AccessKeyID: result.Key.ID, SecretAccessKey: result.Key.Secret})
}

func policy(actions []string, resources []string) any {
	return map[string]any{"Version": "2012-10-17", "Statement": []any{map[string]any{"Effect": "Allow", "Action": actions, "Resource": resources}}}
}

func names() sqsadapter.QueueNames {
	return (sqsadapter.Config{QueuePrefix: "poc-" + uuid.NewString()[:8] + "-"}).QueueNames()
}
func arn(name string) string { return "arn:aws:sqs:" + region + ":000000000000:" + name }

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

func TestMiniStackIAM(t *testing.T) {
	admin := root(t)
	n := names()
	provisioner := user(t, admin, "provisioner", policy([]string{"sqs:CreateQueue", "sqs:SetQueueAttributes", "sqs:GetQueueAttributes", "sqs:GetQueueUrl"}, []string{arn(n.Wager), arn(n.DLQ), arn(n.Events)}))
	// Keep creation and SetQueueAttributes as separate recorded observations.
	for _, name := range []string{n.DLQ, n.Wager, n.Events} {
		_, err := provisioner.client.CreateQueue(t.Context(), &sqs.CreateQueueInput{QueueName: aws.String(name), Attributes: map[string]string{"FifoQueue": "true"}})
		record(t, provisioner, "CreateQueue", arn(name), true, err)
		if err != nil {
			t.FailNow()
		}
	}
	q, err := sqsadapter.ProvisionNamed(t.Context(), provisioner.client, n)
	if err != nil {
		t.Fatal(err)
	}
	_, err = provisioner.client.SetQueueAttributes(t.Context(), &sqs.SetQueueAttributesInput{QueueUrl: aws.String(q.WagerQueueURL), Attributes: map[string]string{"VisibilityTimeout": "30"}})
	record(t, provisioner, "SetQueueAttributes", arn(n.Wager), true, err)
	producer := user(t, admin, "producer", policy([]string{"sqs:SendMessage", "sqs:GetQueueUrl"}, []string{arn(n.Wager)}))
	consumer := user(t, admin, "consumer", policy([]string{"sqs:ReceiveMessage", "sqs:DeleteMessage", "sqs:ChangeMessageVisibility", "sqs:GetQueueAttributes", "sqs:GetQueueUrl"}, []string{arn(n.Wager)}))
	publisher := user(t, admin, "publisher", policy([]string{"sqs:SendMessage", "sqs:GetQueueUrl", "sqs:GetQueueAttributes"}, []string{arn(n.Events)}))
	_, err = send(t, producer, q.WagerQueueURL, "iam-fixture", "iam", uuid.NewString())
	record(t, producer, "SendMessage", arn(n.Wager), true, err)
	_, err = receive(t, producer, q.WagerQueueURL, 0)
	record(t, producer, "ReceiveMessage", arn(n.Wager), false, err)
	got, err := receive(t, consumer, q.WagerQueueURL, 30)
	record(t, consumer, "ReceiveMessage", arn(n.Wager), true, err)
	if err != nil || len(got.Messages) != 1 {
		t.Fatal("consumer did not receive fixture")
	}
	receipt := got.Messages[0].ReceiptHandle
	_, err = producer.client.DeleteMessage(t.Context(), &sqs.DeleteMessageInput{QueueUrl: aws.String(q.WagerQueueURL), ReceiptHandle: receipt})
	record(t, producer, "DeleteMessage", arn(n.Wager), false, err)
	_, err = consumer.client.DeleteMessage(t.Context(), &sqs.DeleteMessageInput{QueueUrl: aws.String(q.WagerQueueURL), ReceiptHandle: receipt})
	record(t, consumer, "DeleteMessage", arn(n.Wager), true, err)
	_, err = send(t, publisher, q.EventsQueueURL, "event-fixture", "events", uuid.NewString())
	record(t, publisher, "SendMessage", arn(n.Events), true, err)
	_, err = send(t, publisher, q.WagerQueueURL, "forbidden", "iam", uuid.NewString())
	record(t, publisher, "SendMessage", arn(n.Wager), false, err)
	_, err = receive(t, consumer, q.EventsQueueURL, 0)
	record(t, consumer, "ReceiveMessage", arn(n.Events), false, err)
	for _, who := range []identity{
		newIdentity(t, "anonymous", aws.Credentials{}),
		newIdentity(t, "unknown-key", aws.Credentials{AccessKeyID: "poc-unknown-key", SecretAccessKey: "wrong-secret"}),
		newIdentity(t, "producer-wrong-secret", aws.Credentials{AccessKeyID: producer.key.AccessKeyID, SecretAccessKey: "wrong-secret"}),
		newIdentity(t, "builtin-test-wrong-secret", aws.Credentials{AccessKeyID: "test", SecretAccessKey: "wrong-secret"}),
		newIdentity(t, "account-id-wrong-secret", aws.Credentials{AccessKeyID: "000000000000", SecretAccessKey: "wrong-secret"}),
	} {
		if who.name == "unknown-key" {
			_, err = receive(t, who, q.WagerQueueURL, 0)
			record(t, who, "ReceiveMessage", arn(n.Wager), false, err)
		} else {
			_, err = send(t, who, q.WagerQueueURL, "forbidden-"+who.name, "iam", uuid.NewString())
			record(t, who, "SendMessage", arn(n.Wager), false, err)
		}
	}
	_, err = consumer.client.DeleteQueue(t.Context(), &sqs.DeleteQueueInput{QueueUrl: aws.String(q.WagerQueueURL)})
	record(t, consumer, "DeleteQueue", arn(n.Wager), false, err)
}

func restart(t *testing.T, abrupt bool) {
	t.Helper()
	project := os.Getenv("MINISTACK_POC_PROJECT")
	if !regexp.MustCompile(`^jungle-ministack-poc-[a-z0-9-]+$`).MatchString(project) {
		t.Fatal("explicit jungle-ministack-poc-* project required")
	}
	run := func(args ...string) {
		ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
		defer cancel()
		base := []string{"compose", "-p", project, "-f", "../../compose.ministack.yaml"}
		if out, err := exec.CommandContext(ctx, "docker", append(base, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("broker lifecycle %v: %v %s", args, err, out)
		}
	}
	if abrupt {
		run("kill", "-s", "KILL", "ministack")
		// Compose kill can return before Docker has observed process exit.
		// Starting in that window is a no-op, so wait for the actual exit first.
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "docker", "wait", project+"-ministack-1").CombinedOutput()
		if err != nil || strings.TrimSpace(string(out)) != "137" {
			t.Fatalf("SIGKILL exit precondition: %v %s", err, out)
		}
		t.Log("broker exited with code 137 before restart")
		run("start", "ministack")
	} else {
		run("restart", "-t", "30", "ministack")
	}
	client := &http.Client{Transport: &transport{}, Timeout: 2 * time.Second}
	for i := 0; i < 60; i++ {
		res, err := client.Get(endpoint(t) + "/_ministack/health")
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

func TestMiniStackPersistence(t *testing.T) {
	for _, abrupt := range []bool{false, true} {
		t.Run(fmt.Sprintf("sigkill=%v", abrupt), func(t *testing.T) {
			admin := root(t)
			n := names()
			q, err := sqsadapter.ProvisionNamed(t.Context(), admin.client, n)
			if err != nil {
				t.Fatal(err)
			}
			// Commit a baseline snapshot BEFORE sending: abrupt recovery must not
			// silently restore the old empty queue and call that durability.
			if abrupt {
				restart(t, false)
			}
			ids := make([]string, 3)
			bodies := []string{"visible-A", "visible-B", "inflight-C"}
			groups := []string{"ordered", "ordered", "inflight"}
			dedup := []string{uuid.NewString(), uuid.NewString(), uuid.NewString()}
			for i := range bodies {
				out, err := send(t, admin, q.EventsQueueURL, bodies[i], groups[i], dedup[i])
				if err != nil {
					t.Fatal(err)
				}
				ids[i] = aws.ToString(out.MessageId)
				t.Logf("accepted id=%s body=%s group=%s dedup=%s HTTP=%d", ids[i], bodies[i], groups[i], dedup[i], admin.wire.status.Load())
			}
			// First message is received but deliberately NOT deleted.
			visibleAfter := time.Now().Add(30 * time.Second)
			inflight, err := receive(t, admin, q.EventsQueueURL, 30)
			if err != nil || len(inflight.Messages) != 1 || aws.ToString(inflight.Messages[0].MessageId) != ids[0] {
				t.Fatalf("inflight precondition: %v", err)
			}
			restart(t, abrupt)
			resolved, err := admin.client.GetQueueUrl(t.Context(), &sqs.GetQueueUrlInput{QueueName: aws.String(n.Events)})
			if err != nil || aws.ToString(resolved.QueueUrl) != q.EventsQueueURL {
				t.Fatalf("queue lost after restart: %v", err)
			}
			seen := map[string]bool{}
			deadline := time.Now().Add(40 * time.Second)
			for time.Now().Before(deadline) && len(seen) < len(ids) {
				out, err := receive(t, admin, q.EventsQueueURL, 2)
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
					if index == 1 && !seen[ids[0]] {
						t.Error("FIFO order violated")
					}
					if index == 0 && m.Attributes["ApproximateReceiveCount"] != "2" {
						t.Errorf("receive count not retained: %v", m.Attributes)
					}
					if index == 0 && time.Now().Before(visibleAfter) {
						t.Error("restart made an in-flight message visible before its visibility timeout")
					}
					seen[id] = true
					t.Logf("recovered id=%s body=%s group=%s dedup=%s receives=%s", id, aws.ToString(m.Body), m.Attributes["MessageGroupId"], m.Attributes["MessageDeduplicationId"], m.Attributes["ApproximateReceiveCount"])
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
			if !abrupt && len(seen) == len(ids) {
				duplicate, err := send(t, admin, q.EventsQueueURL, bodies[0], groups[0], dedup[0])
				if err != nil || aws.ToString(duplicate.MessageId) != ids[0] {
					t.Fatalf("deduplication identity lost after restart: %v", err)
				}
				out, err := receive(t, admin, q.EventsQueueURL, 0)
				if err != nil || len(out.Messages) != 0 {
					t.Fatalf("deduplicated message delivered again: %v", err)
				}
				t.Log("FIFO deduplication retained across graceful restart and DeleteMessage")
			}
		})
	}
}

func TestMiniStackDLQ(t *testing.T) {
	admin := root(t)
	q, err := sqsadapter.ProvisionNamed(t.Context(), admin.client, names())
	if err != nil {
		t.Fatal(err)
	}
	dedup := uuid.NewString()
	sent, err := send(t, admin, q.WagerQueueURL, "poison-message", "dlq-group", dedup)
	if err != nil {
		t.Fatal(err)
	}
	// VisibilityTimeout=0 is NOT honored by this broker: the receive falls back
	// to the queue default (30s) and the source never redelivers within the
	// gate. The runtime always leases with a positive visibility, so the probe
	// uses 1s leases with waits; the redrive gate itself is unchanged.
	for i := 0; i < 3; i++ {
		if i > 0 {
			time.Sleep(1500 * time.Millisecond)
		}
		out, err := receive(t, admin, q.WagerQueueURL, 1)
		if err != nil || len(out.Messages) != 1 {
			t.Fatalf("delivery %d: %v", i+1, err)
		}
		if aws.ToString(out.Messages[0].MessageId) != aws.ToString(sent.MessageId) {
			t.Fatal("changed message identity")
		}
	}
	time.Sleep(1500 * time.Millisecond)
	// Next receive triggers redrive; the source must no longer deliver it.
	out, err := receive(t, admin, q.WagerQueueURL, 1)
	if err != nil || len(out.Messages) != 0 {
		t.Fatalf("redrive failed: %v", err)
	}
	restart(t, false)
	dlq, err := receive(t, admin, q.DLQQueueURL, 30)
	if err != nil || len(dlq.Messages) != 1 {
		t.Fatalf("DLQ empty: %v", err)
	}
	m := dlq.Messages[0]
	if aws.ToString(m.Body) != "poison-message" || aws.ToString(m.MessageId) != aws.ToString(sent.MessageId) || m.Attributes["MessageGroupId"] != "dlq-group" || m.Attributes["MessageDeduplicationId"] != dedup {
		t.Fatalf("DLQ changed identity/body/attributes: %v", m)
	}
	t.Logf("DLQ recovered id=%s body=%s group=%s dedup=%s", aws.ToString(m.MessageId), aws.ToString(m.Body), m.Attributes["MessageGroupId"], m.Attributes["MessageDeduplicationId"])
}
