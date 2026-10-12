package sqs

import (
	"context"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/ports"
)

var _ ports.EventPublisher = (*EventPublisher)(nil)

type EventPublisher struct {
	Client   *sqs.Client
	QueueURL string
}

func (p *EventPublisher) Publish(ctx context.Context, event ports.OutboxDelivery) error {
	_, err := p.Client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl: aws.String(p.QueueURL), MessageBody: aws.String(string(event.Payload)),
		MessageGroupId:         aws.String(event.AggregateID.String()),
		MessageDeduplicationId: aws.String(event.EventID.String()),
	})
	return err
}
