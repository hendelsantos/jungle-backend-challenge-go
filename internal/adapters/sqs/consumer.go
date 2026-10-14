package sqs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/aws/smithy-go"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/command"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/ports"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/failpoint"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/observability"
)

// ConsumeWagerMessages is the single entry point the consumer loop needs;
// injected as a method value, mirroring the reference worker design.
type ConsumeWagerMessages func(ctx context.Context, input command.ConsumeWagerInput) (command.ConsumeWagerResult, error)

// Consumer polls the intake FIFO queue and delegates to the consume command.
// A message is deleted only after its durable handling committed. Permanent
// failures are dead-lettered explicitly; transient failures keep the message
// invisible until the visibility timeout expires, and the queue redrive
// policy moves it to the DLQ after maxReceiveCount.
type Consumer struct {
	Consume       ConsumeWagerMessages
	Client        *sqs.Client
	WagerQueueURL string
	DLQQueueURL   string
	ConsumerName  string
	Logger        *slog.Logger
	ProviderKeys  ProviderKeys
	// Metrics is optional; when present it counts redeliveries and DLQ copies.
	Metrics *observability.FinancialMetrics
	// PostCommitPause holds the consumer between the durable commit and the
	// DeleteMessage broker call, creating an observable kill window for the
	// failure drills. Test instrumentation only: zero in every production
	// configuration.
	PostCommitPause time.Duration
	// PollWait is the long-poll wait in seconds (default 10).
	PollWait int
}

// Run polls until the context is cancelled. Cancellation aborts the in-flight
// receive and any uncommitted processing: the transaction rolls back, the
// message reappears after the visibility timeout and redelivers safely.
func (c *Consumer) Run(ctx context.Context) {
	wait := int32(c.PollWait)
	if wait <= 0 {
		wait = 10
	}
	for {
		if ctx.Err() != nil {
			return
		}
		receiveCtx, cancel := context.WithTimeout(ctx, receiveTimeout)
		out, err := c.Client.ReceiveMessage(receiveCtx, &sqs.ReceiveMessageInput{
			QueueUrl:              aws.String(c.WagerQueueURL),
			MaxNumberOfMessages:   1,
			VisibilityTimeout:     30,
			MessageAttributeNames: []string{"All"},
			WaitTimeSeconds:       wait,
			MessageSystemAttributeNames: []types.MessageSystemAttributeName{
				types.MessageSystemAttributeNameMessageDeduplicationId,
				types.MessageSystemAttributeNameMessageGroupId,
				types.MessageSystemAttributeNameApproximateReceiveCount,
			},
		})
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			c.logError("receive failed", err)
			if !sleepCtx(ctx, time.Second) {
				return
			}
			continue
		}
		for _, message := range out.Messages {
			if ctx.Err() != nil {
				return
			}
			c.handle(ctx, message)
		}
	}
}

// handle processes one message: parse, consume, then delete. Deletion happens
// strictly after the commit; on permanent failure the message is copied to
// the DLQ and then deleted; on transient failure it is left to the visibility
// timeout and the redrive policy.
func (c *Consumer) handle(parent context.Context, message types.Message) {
	// One message per receive avoids aging a local batch beyond its visibility.
	// Cancellation rolls back uncommitted work; release promptly on SIGTERM.
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	defer func() {
		if parent.Err() != nil {
			releaseCtx, release := context.WithTimeout(context.WithoutCancel(parent), 2*time.Second)
			defer release()
			c.changeVisibility(releaseCtx, message, 0)
		}
	}()
	body := []byte("")
	if message.Body != nil {
		body = []byte(*message.Body)
	}
	if c.Metrics != nil {
		if receives, err := strconv.Atoi(message.Attributes[string(types.MessageSystemAttributeNameApproximateReceiveCount)]); err == nil && receives > 1 {
			c.Metrics.Redeliveries.Inc()
		}
	}
	input, err := ParseEnvelope(body, c.ConsumerName)
	if err != nil {
		// Permanently invalid envelope: no domain evaluation happened.
		c.deadLetter(ctx, message, body, err)
		return
	}
	provider, err := c.ProviderKeys.Authenticate(body, message.MessageAttributes)
	if err != nil || provider != input.Wager.ProviderID {
		c.deadLetter(ctx, message, body, ErrUnauthorizedProducer)
		return
	}
	started := time.Now()
	result, err := c.Consume(ctx, input)
	if c.Metrics != nil {
		c.Metrics.Duration.WithLabelValues(observability.MetricKind(string(input.Wager.Kind))).Observe(time.Since(started).Seconds())
	}
	if err != nil {
		if c.Metrics != nil && errors.Is(err, ports.ErrIdempotencyConflict) {
			c.Metrics.Conflicts.Inc()
		}
		if errors.Is(err, command.ErrPermanentMessage) {
			c.deadLetter(ctx, message, body, err)
			return
		}
		// Transient or unknown infrastructure failure: do not delete. The
		// visibility timeout returns the message; redrive moves it to the DLQ
		// after maxReceiveCount.
		c.logMessage("message left for redelivery", input.MessageID, err)
		// A cancelled processing deadline still permits a bounded broker call;
		// no commit acknowledgement is inferred from a timeout.
		retryCtx, cancel := context.WithTimeout(context.WithoutCancel(parent), 2*time.Second)
		defer cancel()
		count, _ := strconv.Atoi(message.Attributes["ApproximateReceiveCount"])
		if count < 1 {
			count = 1
		}
		if count > 5 {
			count = 5
		}
		c.changeVisibility(retryCtx, message, int32(1<<(count-1)))
		return
	}
	if failpoint.Enabled && c.PostCommitPause > 0 {
		failpoint.Hold(parent, c.Logger, "after-commit-before-delete", "messageId", input.MessageID, "sqsMessageId", aws.ToString(message.MessageId), "transactionId", result.TransactionID)
		return
	}
	// Semantics mirror the HTTP boundary: a durable conclusion counts once in
	// Operations; business replays and inbox replays (redelivery of an already
	// completed message) count only in Replays — never a second effect.
	if c.Metrics != nil {
		if result.Replay || result.InboxReplay {
			c.Metrics.Replays.Inc()
		} else {
			c.Metrics.Operations.WithLabelValues(string(input.Wager.Kind), string(result.Status)).Inc()
		}
	}
	if c.Logger != nil {
		c.Logger.Info("wager message consumed",
			"messageId", input.MessageID, "sqsMessageId", aws.ToString(message.MessageId), "receiveCount", message.Attributes["ApproximateReceiveCount"], "correlationId", input.Wager.CorrelationID, "transactionId", result.TransactionID,
			"providerId", input.Wager.ProviderID, "walletId", input.Wager.WalletID,
			"status", result.Status, "inboxReplay", result.InboxReplay, "replay", result.Replay)
	}
	c.delete(ctx, message)
}

// deadLetter copies the message to the DLQ and deletes it from the intake
// queue. The DLQ copy keeps the original body for inspection; the group id
// preserves FIFO ordering semantics on the DLQ. The Dlq counter increments
// only after the broker confirms the copy: an attempt that fails is not
// counted as an arrival (the automatic broker redrive never passes through
// this counter either).
func (c *Consumer) deadLetter(ctx context.Context, message types.Message, body []byte, cause error) {
	messageID := "unknown"
	if message.MessageId != nil {
		messageID = *message.MessageId
	}
	groupID := messageID
	if message.Attributes != nil {
		if id, ok := message.Attributes[string(types.MessageSystemAttributeNameMessageGroupId)]; ok && id != "" {
			groupID = id
		}
	}
	_, err := c.Client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:               aws.String(c.DLQQueueURL),
		MessageBody:            aws.String(string(body)),
		MessageGroupId:         aws.String(groupID),
		MessageDeduplicationId: aws.String(hashBody([]byte(messageID))),
		MessageAttributes:      message.MessageAttributes,
	})
	if err != nil {
		if c.Metrics != nil {
			c.Metrics.DlqFailures.Inc()
		}
		// Keep the original message: it will be redelivered and dead-lettered
		// again, or the redrive policy will move it after maxReceiveCount.
		c.logError(fmt.Sprintf("dead-letter send failed for %s", messageID), err)
		return
	}
	if c.Metrics != nil {
		c.Metrics.Dlq.Inc()
	}
	if c.Logger != nil {
		c.Logger.Warn("message dead-lettered", "messageId", messageID, "reason", failureCode(cause))
	}
	c.delete(ctx, message)
}

func (c *Consumer) delete(ctx context.Context, message types.Message) {
	_, err := c.Client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(c.WagerQueueURL),
		ReceiptHandle: message.ReceiptHandle,
	})
	if err != nil {
		// The durable handling committed; redelivery resolves as an inbox
		// replay and deletes then. Never a duplicate financial effect.
		c.logError("delete after commit failed; message will redeliver as inbox replay", err)
	}
}

func (c *Consumer) logError(message string, err error) {
	if c.Logger != nil {
		c.Logger.Error(message, "errorCode", failureCode(err))
	}
}

func (c *Consumer) logMessage(message, messageID string, err error) {
	if c.Logger != nil {
		c.Logger.Error(message, "messageId", messageID, "errorCode", failureCode(err))
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (c *Consumer) changeVisibility(ctx context.Context, message types.Message, seconds int32) {
	_, err := c.Client.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
		QueueUrl: aws.String(c.WagerQueueURL), ReceiptHandle: message.ReceiptHandle, VisibilityTimeout: seconds,
	})
	if err != nil {
		c.logError("change message visibility failed", err)
	}
}

// Stable diagnostics exclude driver errors that may embed payloads or SQL data.
func failureCode(err error) string {
	switch {
	case errors.Is(err, ErrUnauthorizedProducer):
		return "UNAUTHORIZED_PRODUCER"
	case errors.Is(err, ErrInvalidEnvelope):
		return "INVALID_ENVELOPE"
	case errors.Is(err, ports.ErrInboxConflict):
		return "INBOX_CONFLICT"
	case errors.Is(err, ports.ErrIdempotencyConflict):
		return "IDEMPOTENCY_CONFLICT"
	case errors.Is(err, command.ErrPermanentMessage):
		return "INVALID_OPERATION"
	case errors.Is(err, context.Canceled):
		return "CANCELLED"
	case errors.Is(err, context.DeadlineExceeded):
		return "DEADLINE_EXCEEDED"
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode()
	}
	return "INFRASTRUCTURE_ERROR"
}
