package sqsqueue

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"imageforge/internal/ports"
)

const testQueueURL = "https://sqs.eu-west-1.amazonaws.com/000000000000/imageforge-jobs"

var errTransient = errors.New("service unavailable")

// fakeSQS is a scripted SQS, so the receive loop and its backoff can be tested
// without a broker.
type fakeSQS struct {
	mu sync.Mutex

	// receiveResults is consumed one entry per ReceiveMessage call. Once it is
	// exhausted, receiveDefault is returned forever.
	receiveResults []receiveResult
	receiveDefault receiveResult

	receiveCalls    atomic.Int64
	deleted         []string
	visibilitySet   []string
	sent            []string
	depthValue      string
	depthErr        error
	getQueueURLErr  error
	deleteMessageEr error
}

// receiveResult is one scripted answer to ReceiveMessage.
type receiveResult struct {
	bodies []string
	err    error
}

//nolint:revive,staticcheck // the name mirrors the AWS SDK method this fake stands in for.
func (f *fakeSQS) GetQueueUrl(
	_ context.Context, _ *sqs.GetQueueUrlInput, _ ...func(*sqs.Options),
) (*sqs.GetQueueUrlOutput, error) {
	if f.getQueueURLErr != nil {
		return nil, f.getQueueURLErr
	}
	return &sqs.GetQueueUrlOutput{QueueUrl: aws.String(testQueueURL)}, nil
}

func (f *fakeSQS) GetQueueAttributes(
	_ context.Context, _ *sqs.GetQueueAttributesInput, _ ...func(*sqs.Options),
) (*sqs.GetQueueAttributesOutput, error) {
	if f.depthErr != nil {
		return nil, f.depthErr
	}
	if f.depthValue == "" {
		return &sqs.GetQueueAttributesOutput{Attributes: map[string]string{}}, nil
	}
	return &sqs.GetQueueAttributesOutput{Attributes: map[string]string{
		string(types.QueueAttributeNameApproximateNumberOfMessages): f.depthValue,
	}}, nil
}

func (f *fakeSQS) SendMessage(
	_ context.Context, in *sqs.SendMessageInput, _ ...func(*sqs.Options),
) (*sqs.SendMessageOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, aws.ToString(in.MessageBody))
	return &sqs.SendMessageOutput{}, nil
}

func (f *fakeSQS) ReceiveMessage(
	ctx context.Context, _ *sqs.ReceiveMessageInput, _ ...func(*sqs.Options),
) (*sqs.ReceiveMessageOutput, error) {
	n := f.receiveCalls.Add(1)

	f.mu.Lock()
	result := f.receiveDefault
	if int(n) <= len(f.receiveResults) {
		result = f.receiveResults[n-1]
	}
	f.mu.Unlock()

	if result.err != nil {
		return nil, result.err
	}
	if len(result.bodies) == 0 {
		// An empty long poll: block until the caller gives up, as a real one
		// would, rather than spinning.
		<-ctx.Done()
		return nil, ctx.Err()
	}

	messages := make([]types.Message, 0, len(result.bodies))
	for _, body := range result.bodies {
		messages = append(messages, types.Message{
			Body:          aws.String(body),
			ReceiptHandle: aws.String("receipt-" + body),
		})
	}
	return &sqs.ReceiveMessageOutput{Messages: messages}, nil
}

func (f *fakeSQS) DeleteMessage(
	_ context.Context, in *sqs.DeleteMessageInput, _ ...func(*sqs.Options),
) (*sqs.DeleteMessageOutput, error) {
	if f.deleteMessageEr != nil {
		return nil, f.deleteMessageEr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, aws.ToString(in.ReceiptHandle))
	return &sqs.DeleteMessageOutput{}, nil
}

func (f *fakeSQS) ChangeMessageVisibility(
	_ context.Context, in *sqs.ChangeMessageVisibilityInput, _ ...func(*sqs.Options),
) (*sqs.ChangeMessageVisibilityOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.visibilitySet = append(f.visibilitySet, aws.ToString(in.ReceiptHandle))
	return &sqs.ChangeMessageVisibilityOutput{}, nil
}

func (f *fakeSQS) settled() (deleted, visibility []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.deleted...), append([]string(nil), f.visibilitySet...)
}

// newTestQueue builds a queue over the fake with test-sized backoff bounds.
func newTestQueue(t *testing.T, client *fakeSQS, opts ...Option) *Queue {
	t.Helper()

	opts = append([]Option{
		WithWaitTime(time.Second),
		WithBackoff(time.Millisecond, 4*time.Millisecond),
	}, opts...)

	queue, err := New(context.Background(), client, "imageforge-jobs", opts...)
	require.NoError(t, err)
	return queue
}

func TestBackoffNext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		base     time.Duration
		max      time.Duration
		attempts int
		wantCap  time.Duration
	}{
		{name: "first failure caps at the base", base: time.Second, max: time.Minute, attempts: 1, wantCap: time.Second},
		{name: "second doubles", base: time.Second, max: time.Minute, attempts: 2, wantCap: 2 * time.Second},
		{name: "third doubles again", base: time.Second, max: time.Minute, attempts: 3, wantCap: 4 * time.Second},
		{name: "growth stops at the ceiling", base: time.Second, max: 5 * time.Second, attempts: 10, wantCap: 5 * time.Second},
		{name: "a long outage does not overflow", base: time.Second, max: time.Minute, attempts: 200, wantCap: time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Draw the top of the jitter range, so the assertion is about the
			// ceiling rather than about a random draw.
			b := newBackoff(tt.base, tt.max)
			b.random = func(n int64) int64 { return n - 1 }

			var last time.Duration
			for range tt.attempts {
				last = b.next()
			}

			assert.Positive(t, last)
			assert.LessOrEqual(t, last, tt.wantCap, "the delay must not exceed the cap for this attempt")
			assert.Equal(t, tt.attempts, b.attempts())
		})
	}
}

// TestBackoffGrowsThenResets pins the shape of the sequence: it climbs while
// failures continue and drops back to the base once one succeeds.
func TestBackoffGrowsThenResets(t *testing.T) {
	t.Parallel()

	b := newBackoff(time.Second, time.Minute)
	b.random = func(n int64) int64 { return n - 1 } // always the top of the range

	first := b.next()
	second := b.next()
	third := b.next()

	assert.Greater(t, second, first, "the delay grows with consecutive failures")
	assert.Greater(t, third, second)

	b.reset()
	assert.Zero(t, b.attempts())
	assert.LessOrEqual(t, b.next(), time.Second, "a success returns to the base delay")
}

// TestBackoffJitterSpreadsRetries checks the jitter actually varies, which is
// what stops a fleet of workers retrying in lockstep.
func TestBackoffJitterSpreadsRetries(t *testing.T) {
	t.Parallel()

	seen := make(map[time.Duration]struct{})
	for range 50 {
		b := newBackoff(time.Second, time.Minute)
		b.attempt = 4 // a wide range to draw from
		seen[b.next()] = struct{}{}
	}

	assert.Greater(t, len(seen), 1, "jittered delays must not all be identical")
}

func TestWaitReturnsEarlyWhenCanceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	assert.False(t, wait(ctx, time.Hour), "a canceled context must not wait")
	assert.True(t, wait(context.Background(), 0), "a zero delay does not block")
}

// TestConsumeRetriesUntilTheQueueRecovers is the retry case: the first receives
// fail, and the consumer must keep trying and deliver the messages that arrive
// once the queue comes back.
func TestConsumeRetriesUntilTheQueueRecovers(t *testing.T) {
	t.Parallel()

	client := &fakeSQS{
		receiveResults: []receiveResult{
			{err: errTransient},
			{err: errTransient},
			{err: errTransient},
			{bodies: []string{"job-1", "job-2"}},
		},
	}

	var (
		mu       sync.Mutex
		attempts []int
		delays   []time.Duration
	)
	queue := newTestQueue(t, client, WithReceiveErrorHandler(
		func(_ error, attempt int, delay time.Duration) {
			mu.Lock()
			defer mu.Unlock()
			attempts = append(attempts, attempt)
			delays = append(delays, delay)
		}))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ids, err := queue.Consume(ctx)
	require.NoError(t, err)

	assert.Equal(t, "job-1", <-ids)
	assert.Equal(t, "job-2", <-ids)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []int{1, 2, 3}, attempts, "each failure is reported with its consecutive count")
	require.Len(t, delays, 3)
	for _, d := range delays {
		assert.LessOrEqual(t, d, 4*time.Millisecond, "the delay respects the configured ceiling")
	}
}

// TestConsumeResetsBackoffAfterSuccess checks a later failure starts again from
// the base rather than from wherever the previous outage left off.
func TestConsumeResetsBackoffAfterSuccess(t *testing.T) {
	t.Parallel()

	client := &fakeSQS{
		receiveResults: []receiveResult{
			{err: errTransient},
			{err: errTransient},
			{bodies: []string{"job-1"}},
			{err: errTransient},
			{bodies: []string{"job-2"}},
		},
	}

	var (
		mu       sync.Mutex
		attempts []int
	)
	queue := newTestQueue(t, client, WithReceiveErrorHandler(
		func(_ error, attempt int, _ time.Duration) {
			mu.Lock()
			defer mu.Unlock()
			attempts = append(attempts, attempt)
		}))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ids, err := queue.Consume(ctx)
	require.NoError(t, err)

	assert.Equal(t, "job-1", <-ids)
	assert.Equal(t, "job-2", <-ids)

	mu.Lock()
	defer mu.Unlock()
	// 1, 2 for the first outage, then 1 again: the counter reset on success.
	assert.Equal(t, []int{1, 2, 1}, attempts)
}

// TestConsumeStopsOnCancel covers the shutdown half: a canceled context stops
// the polling loop and closes the channel, whether the queue is healthy or not.
func TestConsumeStopsOnCancel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		client *fakeSQS
	}{
		{
			name:   "while long-polling an empty queue",
			client: &fakeSQS{receiveDefault: receiveResult{}},
		},
		{
			name:   "while backing off from a failing queue",
			client: &fakeSQS{receiveDefault: receiveResult{err: errTransient}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			queue := newTestQueue(t, tt.client,
				// A long backoff proves cancellation interrupts the wait rather
				// than the test outlasting it.
				WithBackoff(time.Hour, time.Hour))

			ctx, cancel := context.WithCancel(context.Background())
			ids, err := queue.Consume(ctx)
			require.NoError(t, err)

			// Let the loop reach its first receive before pulling the rug out.
			assert.Eventually(t, func() bool {
				return tt.client.receiveCalls.Load() > 0
			}, 10*time.Second, time.Millisecond)

			cancel()

			select {
			case _, open := <-ids:
				assert.False(t, open, "the channel must be closed, not delivering")
			case <-time.After(30 * time.Second):
				t.Fatal("Consume did not stop after its context was canceled")
			}
		})
	}
}

// TestAckDeletesTheMessage covers delete-on-success at the adapter level.
func TestAckDeletesTheMessage(t *testing.T) {
	t.Parallel()

	client := &fakeSQS{receiveResults: []receiveResult{{bodies: []string{"job-1"}}}}
	queue := newTestQueue(t, client)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ids, err := queue.Consume(ctx)
	require.NoError(t, err)
	require.Equal(t, "job-1", <-ids)
	assert.Equal(t, 1, queue.InFlight())

	require.NoError(t, queue.Ack(ctx, "job-1"))

	deleted, visibility := client.settled()
	assert.Equal(t, []string{"receipt-job-1"}, deleted)
	assert.Empty(t, visibility)
	assert.Zero(t, queue.InFlight(), "an acknowledged delivery is no longer tracked")
}

// TestNackReturnsTheMessage covers the other settlement path.
func TestNackReturnsTheMessage(t *testing.T) {
	t.Parallel()

	client := &fakeSQS{receiveResults: []receiveResult{{bodies: []string{"job-1"}}}}
	queue := newTestQueue(t, client)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ids, err := queue.Consume(ctx)
	require.NoError(t, err)
	require.Equal(t, "job-1", <-ids)

	require.NoError(t, queue.Nack(ctx, "job-1"))

	deleted, visibility := client.settled()
	assert.Empty(t, deleted)
	assert.Equal(t, []string{"receipt-job-1"}, visibility, "the message is made visible again")
	assert.Zero(t, queue.InFlight())
}

// TestSettlingAnUnknownDeliveryIsNotAnError documents the deliberate leniency:
// a delivery whose visibility expired belongs to another consumer now.
func TestSettlingAnUnknownDeliveryIsNotAnError(t *testing.T) {
	t.Parallel()

	client := &fakeSQS{}
	queue := newTestQueue(t, client)

	assert.NoError(t, queue.Ack(context.Background(), "never-seen"))
	assert.NoError(t, queue.Nack(context.Background(), "never-seen"))

	deleted, visibility := client.settled()
	assert.Empty(t, deleted)
	assert.Empty(t, visibility)
}

func TestDepth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		client  *fakeSQS
		want    int
		wantErr bool
	}{
		{name: "reports the approximate count", client: &fakeSQS{depthValue: "42"}, want: 42},
		{name: "an empty queue is zero", client: &fakeSQS{depthValue: "0"}, want: 0},
		{name: "a missing attribute is an error", client: &fakeSQS{}, wantErr: true},
		{name: "a non-numeric value is an error", client: &fakeSQS{depthValue: "many"}, wantErr: true},
		{name: "an api failure is an error", client: &fakeSQS{depthErr: errTransient}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			queue := newTestQueue(t, tt.client)

			depth, err := queue.Depth(context.Background())

			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, depth)
		})
	}
}

func TestEnqueue(t *testing.T) {
	t.Parallel()

	client := &fakeSQS{}
	queue := newTestQueue(t, client)

	require.NoError(t, queue.Enqueue(context.Background(), "job-1"))
	require.Error(t, queue.Enqueue(context.Background(), ""), "an empty id is rejected")

	client.mu.Lock()
	defer client.mu.Unlock()
	assert.Equal(t, []string{"job-1"}, client.sent)
}

func TestNewResolvesTheQueueURL(t *testing.T) {
	t.Parallel()

	t.Run("a name is resolved", func(t *testing.T) {
		t.Parallel()

		queue, err := New(context.Background(), &fakeSQS{}, "imageforge-jobs")
		require.NoError(t, err)
		assert.Equal(t, testQueueURL, queue.URL())
	})

	t.Run("a url is used as given", func(t *testing.T) {
		t.Parallel()

		queue, err := New(context.Background(), &fakeSQS{}, testQueueURL)
		require.NoError(t, err)
		assert.Equal(t, testQueueURL, queue.URL())
	})

	t.Run("a failed lookup is reported", func(t *testing.T) {
		t.Parallel()

		_, err := New(context.Background(), &fakeSQS{getQueueURLErr: errTransient}, "imageforge-jobs")
		require.ErrorIs(t, err, errTransient)
	})

	t.Run("a nil client is rejected", func(t *testing.T) {
		t.Parallel()

		_, err := New(context.Background(), nil, "imageforge-jobs")
		require.Error(t, err)
	})

	t.Run("an empty name is rejected", func(t *testing.T) {
		t.Parallel()

		_, err := New(context.Background(), &fakeSQS{}, "  ")
		require.Error(t, err)
	})
}

func TestOptionsGuardAgainstMeaninglessValues(t *testing.T) {
	t.Parallel()

	queue := newTestQueue(t, &fakeSQS{},
		WithWaitTime(-1),
		WithVisibilityTimeout(0),
		WithMaxMessages(0),
		WithBackoff(-1, -1),
		WithReceiveErrorHandler(nil),
	)

	assert.Equal(t, time.Second, queue.waitTime, "the earlier option stands")
	assert.Equal(t, DefaultVisibilityTimeout, queue.visibilityTimeout)
	assert.Equal(t, int32(DefaultMaxMessages), queue.maxMessages)
	assert.Equal(t, time.Millisecond, queue.backoffBase)
	assert.NotNil(t, queue.onReceiveError)

	// The SQS maxima are enforced rather than passed through.
	capped := newTestQueue(t, &fakeSQS{}, WithWaitTime(time.Hour), WithMaxMessages(100))
	assert.Equal(t, 20*time.Second, capped.waitTime)
	assert.Equal(t, int32(10), capped.maxMessages)
}

// TestQueueImplementsOptionalInterfaces guards the assertions the worker relies
// on when it decides whether to settle deliveries and report depth.
func TestQueueImplementsOptionalInterfaces(t *testing.T) {
	t.Parallel()

	queue := newTestQueue(t, &fakeSQS{})
	assert.Implements(t, (*ports.Queue)(nil), queue)
	assert.Implements(t, (*ports.Acknowledger)(nil), queue)
	assert.Implements(t, (*ports.DepthReporter)(nil), queue)
}
