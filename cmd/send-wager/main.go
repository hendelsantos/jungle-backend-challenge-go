// send-wager signs and sends the official envelope to the local development
// queue. Provider credentials are supplied through the environment, never flags.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqsadapter "github.com/hendelsantos/jungle-backend-challenge-go/internal/adapters/sqs"
)

func main() {
	file := flag.String("file", "", "path to the official JSON envelope")
	provider := flag.String("provider", "provider-a", "authenticated producer identity")
	flag.Parse()
	if err := run(*file, *provider); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(path, provider string) error {
	key := os.Getenv("SQS_PROVIDER_KEY")
	if err := (sqsadapter.ProviderKeys{provider: key}).Validate(); err != nil {
		return fmt.Errorf("set SQS_PROVIDER_KEY with at least 32 bytes")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, 64*1024+1))
	if err != nil {
		return err
	}
	input, err := sqsadapter.ParseEnvelope(body, "wager-consumer")
	if err != nil {
		return err
	}
	if input.Wager.ProviderID != provider {
		return fmt.Errorf("envelope provider differs from signing identity")
	}
	endpoint, region := os.Getenv("SQS_ENDPOINT_URL"), os.Getenv("SQS_REGION")
	if endpoint == "" {
		endpoint = "http://localhost:4566"
	}
	if region == "" {
		region = "us-east-1"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	prefix := os.Getenv("SQS_QUEUE_PREFIX")
	client, err := sqsadapter.NewClient(ctx, sqsadapter.Config{EndpointURL: endpoint, Region: region, QueuePrefix: prefix})
	if err != nil {
		return err
	}
	queue, err := client.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: aws.String(prefix + sqsadapter.WagerQueueName)})
	if err != nil {
		return err
	}
	// Hashing the exact body fits the SQS dedup-id alphabet. A rewritten body
	// bypasses broker dedup so the durable inbox can report its identity conflict.
	result, err := client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl: queue.QueueUrl, MessageBody: aws.String(string(body)),
		MessageGroupId:         aws.String(input.Wager.WalletID.String()),
		MessageDeduplicationId: aws.String(input.PayloadHash),
		MessageAttributes:      sqsadapter.SignAttributes(provider, key, body),
	})
	if err != nil {
		return err
	}
	fmt.Printf("messageId=%s sqsMessageId=%s\n", input.MessageID, aws.ToString(result.MessageId))
	return nil
}
