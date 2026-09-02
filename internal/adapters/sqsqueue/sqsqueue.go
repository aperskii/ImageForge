// Package sqsqueue implements the ports.Queue port on Amazon SQS.
//
// Deliveries are settled explicitly: the queue also implements
// ports.Acknowledger, so a message is deleted once its job has been processed
// and returned to the queue otherwise. Repeatedly failing messages end up on
// the dead-letter queue through the redrive policy configured on the queue
// itself, not by anything here.
package sqsqueue

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"imageforge/internal/ports"
)

// Compile-time assertions that Queue satisfies both the port and its optional
// companion.
var (
	_ ports.Queue        = (*Queue)(nil)
	_ ports.Acknowledger = (*Queue)(nil)
)

// Defaults applied when no option overrides them.
const (
	// DefaultWaitTime is the long-polling wait. Twenty seconds is the SQS
	// maximum, and the point of long polling: fewer empty receives, and a job
	// picked up as soon as it arrives.
	DefaultWaitTime = 20 * time.Second
	// DefaultVisibilityTimeout is how long a received message stays hidden
	// from other consumers. It must exceed the time a job takes to process,
	// or the job is handed to a second worker while the first is still on it.
	DefaultVisibilityTimeout = 2 * time.Minute
	// DefaultMaxMessages is how many messages one receive asks for. Ten is the
	// SQS maximum.
	DefaultMaxMessages = 10
)

// Queue transports job identifiers over an SQS queue.
//
// It is safe for concurrent use.
type Queue struct {
	client   *sqs.Client
	queueURL string

	waitTime          time.Duration
	visibilityTimeout time.Duration
	maxMessages       int32

	// receipts maps a job identifier to the receipt handle of its delivery, so
	// Ack and Nack can settle it. Keyed by job id because that is all the
	// Queue port carries; a duplicate delivery of the same job therefore
	// replaces the earlier handle, whose message simply reappears when its
	// visibility timeout expires and is skipped as no longer pending.
	mu       sync.Mutex
	receipts map[string]string
}

// Option overrides a Queue setting.
type Option func(*Queue)

// WithWaitTime sets the long-polling wait, capped at the SQS maximum of twenty
// seconds. A non-positive duration leaves the default in place.
func WithWaitTime(d time.Duration) Option {
	return func(q *Queue) {
		if d > 0 {
			q.waitTime = min(d, 20*time.Second)
		}
	}
}

// WithVisibilityTimeout sets how long a received message stays hidden. A
// non-positive duration leaves the default in place.
func WithVisibilityTimeout(d time.Duration) Option {
	return func(q *Queue) {
		if d > 0 {
			q.visibilityTimeout = d
		}
	}
}

// WithMaxMessages sets how many messages one receive asks for, capped at the
// SQS maximum of ten. A non-positive count leaves the default in place.
func WithMaxMessages(n int32) Option {
	return func(q *Queue) {
		if n > 0 {
			q.maxMessages = min(n, 10)
		}
	}
}

// New wires a Queue to an SQS queue, named either by its URL or by its name,
// which is resolved to a URL once here rather than on every call.
func New(ctx context.Context, client *sqs.Client, nameOrURL string, opts ...Option) (*Queue, error) {
	if client == nil {
		return nil, errors.New("sqsqueue: nil client")
	}
	if strings.TrimSpace(nameOrURL) == "" {
		return nil, errors.New("sqsqueue: queue name is empty")
	}

	queueURL := nameOrURL
	if !strings.Contains(nameOrURL, "://") {
		out, err := client.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: aws.String(nameOrURL)})
		if err != nil {
			return nil, fmt.Errorf("sqsqueue: resolve queue %q: %w", nameOrURL, err)
		}
		queueURL = aws.ToString(out.QueueUrl)
	}

	queue := &Queue{
		client:            client,
		queueURL:          queueURL,
		waitTime:          DefaultWaitTime,
		visibilityTimeout: DefaultVisibilityTimeout,
		maxMessages:       DefaultMaxMessages,
		receipts:          make(map[string]string),
	}
	for _, opt := range opts {
		opt(queue)
	}
	return queue, nil
}

// URL returns the resolved queue URL.
func (q *Queue) URL() string { return q.queueURL }

// Enqueue publishes jobID for processing.
func (q *Queue) Enqueue(ctx context.Context, jobID string) error {
	if jobID == "" {
		return errors.New("sqsqueue: empty job id")
	}

	if _, err := q.client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:    aws.String(q.queueURL),
		MessageBody: aws.String(jobID),
	}); err != nil {
		return fmt.Errorf("sqsqueue: enqueue %s: %w", jobID, err)
	}
	return nil
}

// Consume long-polls the queue and returns a channel of job identifiers.
//
// The channel is closed when ctx is done. Each delivery stays hidden from other
// consumers for the visibility timeout and must be settled with Ack or Nack;
// an unsettled delivery reappears once that timeout expires.
func (q *Queue) Consume(ctx context.Context) (<-chan string, error) {
	out := make(chan string)

	go func() {
		defer close(out)
		for {
			if ctx.Err() != nil {
				return
			}
			messages, err := q.receive(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				// A transient receive failure must not kill the consumer. Back
				// off briefly so a persistent one does not spin.
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Second):
				}
				continue
			}

			for _, jobID := range messages {
				select {
				case out <- jobID:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return out, nil
}

// receive performs one long poll and records the receipt handle of every
// message it returns.
func (q *Queue) receive(ctx context.Context) ([]string, error) {
	out, err := q.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(q.queueURL),
		MaxNumberOfMessages: q.maxMessages,
		WaitTimeSeconds:     int32(q.waitTime.Seconds()),
		VisibilityTimeout:   int32(q.visibilityTimeout.Seconds()),
	})
	if err != nil {
		return nil, fmt.Errorf("sqsqueue: receive: %w", err)
	}

	jobIDs := make([]string, 0, len(out.Messages))
	q.mu.Lock()
	for _, msg := range out.Messages {
		jobID := aws.ToString(msg.Body)
		if jobID == "" {
			continue
		}
		q.receipts[jobID] = aws.ToString(msg.ReceiptHandle)
		jobIDs = append(jobIDs, jobID)
	}
	q.mu.Unlock()

	return jobIDs, nil
}

// Ack deletes the message carrying jobID, so it is not delivered again.
//
// An identifier with no recorded delivery is not an error: its visibility
// timeout may have expired and the message been handed to another consumer,
// which is that consumer's to settle.
func (q *Queue) Ack(ctx context.Context, jobID string) error {
	receipt, ok := q.takeReceipt(jobID)
	if !ok {
		return nil
	}

	if _, err := q.client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(q.queueURL),
		ReceiptHandle: aws.String(receipt),
	}); err != nil {
		return fmt.Errorf("sqsqueue: ack %s: %w", jobID, err)
	}
	return nil
}

// Nack returns the message carrying jobID to the queue immediately, rather than
// waiting out its visibility timeout.
//
// Redelivery is what eventually moves a message to the dead-letter queue, once
// it exceeds the receive count in the queue's redrive policy. A job whose
// failure was already recorded is skipped on redelivery, since it is no longer
// pending.
func (q *Queue) Nack(ctx context.Context, jobID string) error {
	receipt, ok := q.takeReceipt(jobID)
	if !ok {
		return nil
	}

	if _, err := q.client.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
		QueueUrl:          aws.String(q.queueURL),
		ReceiptHandle:     aws.String(receipt),
		VisibilityTimeout: 0,
	}); err != nil {
		return fmt.Errorf("sqsqueue: nack %s: %w", jobID, err)
	}
	return nil
}

// takeReceipt removes and returns the receipt handle recorded for jobID.
func (q *Queue) takeReceipt(jobID string) (string, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	receipt, ok := q.receipts[jobID]
	if ok {
		delete(q.receipts, jobID)
	}
	return receipt, ok
}

// InFlight reports how many deliveries are waiting to be settled. It is
// intended for tests and diagnostics.
func (q *Queue) InFlight() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.receipts)
}
