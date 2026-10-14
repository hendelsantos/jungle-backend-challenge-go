package sqs

import (
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/ports"
	"log/slog"
)

var _ ports.EventPublisher = (*EventPublisher)(nil)

type EventPublisher struct {
	Client   *sqs.Client
	QueueURL string
	Logger   *slog.Logger
}

func (p *EventPublisher) Publish(ctx context.Context, event ports.OutboxDelivery) error {
	result, err := p.Client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl: aws.String(p.QueueURL), MessageBody: aws.String(string(event.Payload)),
		MessageGroupId:         aws.String(event.AggregateID.String()),
		MessageDeduplicationId: aws.String(event.EventID.String()),
	})
	if err == nil && p.Logger != nil {
		p.Logger.Info("outbox send accepted", "eventId", event.EventID, "sqsMessageId", aws.ToString(result.MessageId), "payloadSHA256", fmt.Sprintf("%x", sha256.Sum256(event.Payload)), "attempt", event.Attempts)
	}
	return err
}
