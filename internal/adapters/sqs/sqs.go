// Package sqs adapts local SQS FIFO queues to application commands.
package sqs

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// Config covers the local development endpoint and the target region. The
// static credentials are development-only: this client targets LocalStack.
type Config struct {
	EndpointURL string
	Region      string
	QueuePrefix string
}

func (c Config) Validate() error {
	if c.EndpointURL == "" || c.Region == "" {
		return fmt.Errorf("SQS requires an endpoint URL and a region")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{0,32}$`).MatchString(c.QueuePrefix) {
		return fmt.Errorf("SQS queue prefix must be at most 32 letters, digits, hyphens or underscores")
	}
	parsed, err := url.Parse(c.EndpointURL)
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("SQS endpoint must be an HTTP(S) URL without credentials, query or fragment")
	}
	return nil
}

// NewClient builds an SQS client pointed at the configured endpoint. The
// static development credentials require no AWS account or external secrets.
func NewClient(ctx context.Context, config Config) (*sqs.Client, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(config.Region),
		awsconfig.WithHTTPClient(&http.Client{Timeout: 20 * time.Second}),
		awsconfig.WithRetryMaxAttempts(2),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
	)
	if err != nil {
		return nil, fmt.Errorf("sqs: load aws config: %w", err)
	}
	return sqs.NewFromConfig(awsCfg, func(options *sqs.Options) {
		options.BaseEndpoint = aws.String(config.EndpointURL)
	}), nil
}

// QueueNames are the challenge-mandated FIFO queues.
const (
	WagerQueueName   = "wager-transactions.fifo"
	DLQQueueName     = "wager-transactions-dlq.fifo"
	EventsQueueName  = "wager-events.fifo"
	maxReceiveCount  = "3"
	visibilitySecond = "30"
)

// Provisioned carries the resolved queue URLs.
type Provisioned struct {
	WagerQueueURL  string
	DLQQueueURL    string
	EventsQueueURL string
}

// Provision creates the FIFO queues idempotently: the DLQ first, then the
// intake queue with a redrive policy (maxReceiveCount 3, visibility 30s) and
// the events queue for the outbox publisher. Existing queues are reused.
func Provision(ctx context.Context, client *sqs.Client) (Provisioned, error) {
	return ProvisionNamed(ctx, client, QueueNames{WagerQueueName, DLQQueueName, EventsQueueName})
}

// QueueNames allows integration tests to own and delete isolated queues.
type QueueNames struct{ Wager, DLQ, Events string }

func ProvisionNamed(ctx context.Context, client *sqs.Client, names QueueNames) (Provisioned, error) {
	dlqURL, err := ensureFIFOQueue(ctx, client, names.DLQ, nil)
	if err != nil {
		return Provisioned{}, err
	}
	dlqARN, err := queueARN(ctx, client, dlqURL)
	if err != nil {
		return Provisioned{}, err
	}
	wagerURL, err := ensureFIFOQueue(ctx, client, names.Wager, map[string]string{
		"RedrivePolicy":     fmt.Sprintf(`{"deadLetterTargetArn":%q,"maxReceiveCount":%q}`, dlqARN, maxReceiveCount),
		"VisibilityTimeout": visibilitySecond,
	})
	if err != nil {
		return Provisioned{}, err
	}
	eventsURL, err := ensureFIFOQueue(ctx, client, names.Events, nil)
	if err != nil {
		return Provisioned{}, err
	}
	return Provisioned{WagerQueueURL: wagerURL, DLQQueueURL: dlqURL, EventsQueueURL: eventsURL}, nil
}

func ensureFIFOQueue(ctx context.Context, client *sqs.Client, name string, extra map[string]string) (string, error) {
	attributes := map[string]string{"ContentBasedDeduplication": "false"}
	for key, value := range extra {
		attributes[key] = value
	}
	queue, err := queueURL(ctx, client, name)
	if err != nil {
		var missing *types.QueueDoesNotExist
		if !errors.As(err, &missing) {
			return "", err
		}
		creation := map[string]string{"FifoQueue": "true"}
		for k, v := range attributes {
			creation[k] = v
		}
		out, createErr := client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(name), Attributes: creation})
		if createErr != nil {
			return "", fmt.Errorf("sqs: create %s: %w", name, createErr)
		}
		queue = aws.ToString(out.QueueUrl)
	}
	// Reconcile mutable attributes even for existing queues; never silently
	// accept missing redrive or a stale visibility policy.
	if _, err := client.SetQueueAttributes(ctx, &sqs.SetQueueAttributesInput{QueueUrl: aws.String(queue), Attributes: attributes}); err != nil {
		return "", fmt.Errorf("sqs: configure %s: %w", name, err)
	}
	return queue, nil
}

func queueURL(ctx context.Context, client *sqs.Client, name string) (string, error) {
	out, err := client.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: aws.String(name)})
	if err != nil {
		return "", err
	}
	if out.QueueUrl == nil {
		return "", fmt.Errorf("sqs: queue %s has no url", name)
	}
	return *out.QueueUrl, nil
}

func queueARN(ctx context.Context, client *sqs.Client, url string) (string, error) {
	out, err := client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(url),
		AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
	})
	if err != nil {
		return "", fmt.Errorf("sqs: queue arn: %w", err)
	}
	arn := out.Attributes["QueueArn"]
	if arn == "" {
		return "", fmt.Errorf("sqs: queue %s returned no arn", url)
	}
	return arn, nil
}

// receiveTimeout bounds one long-poll call when the caller has no deadline.
const receiveTimeout = 15 * time.Second

// ManagedClient couples the SQS client with its provisioned queue URLs.
// Provisioning happens during construction (fail-fast, like the database
// ping): the application does not start serving without its queues.
type ManagedClient struct {
	*sqs.Client
	Queues Provisioned
}

// NewManagedClient builds the client and provisions the queues, retrying
// while the broker warms up (LocalStack containers take a few seconds).
func NewManagedClient(ctx context.Context, config Config) (*ManagedClient, error) {
	client, err := NewClient(ctx, config)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var queues Provisioned
	for {
		queues, err = ProvisionNamed(ctx, client, QueueNames{config.QueuePrefix + WagerQueueName, config.QueuePrefix + DLQQueueName, config.QueuePrefix + EventsQueueName})
		if err == nil {
			return &ManagedClient{Client: client, Queues: queues}, nil
		}
		if ctx.Err() != nil {
			return nil, fmt.Errorf("sqs: provision queues: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("sqs: provision queues: %w", ctx.Err())
		case <-time.After(time.Second):
		}
	}
}

// Ready probes all queues; the HTTP boundary reports only availability.
func (c *ManagedClient) Ready(ctx context.Context) error {
	for _, url := range []string{c.Queues.WagerQueueURL, c.Queues.DLQQueueURL, c.Queues.EventsQueueURL} {
		if _, err := queueARN(ctx, c.Client, url); err != nil {
			return err
		}
	}
	return nil
}
