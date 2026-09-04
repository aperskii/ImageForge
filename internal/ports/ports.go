package ports

import (
	"context"
	"errors"
	"io"

	"imageforge/internal/domain"
)

// ErrJobNotFound is returned by JobRepository implementations when no job
// exists for the requested identifier.
var ErrJobNotFound = errors.New("job not found")

// Storage persists opaque binary objects under a caller-chosen key.
type Storage interface {
	// Put writes data under key, overwriting any existing object.
	Put(ctx context.Context, key string, data io.Reader) error
	// Get opens the object stored under key. The caller closes the reader.
	Get(ctx context.Context, key string) (io.ReadCloser, error)
}

// Queue transports job identifiers from the API to the workers.
type Queue interface {
	// Enqueue publishes jobID for asynchronous processing.
	Enqueue(ctx context.Context, jobID string) error
	// Consume returns a channel of job identifiers. The channel is closed when
	// ctx is canceled or the underlying consumer stops.
	Consume(ctx context.Context) (<-chan string, error)
}

// Acknowledger is an optional companion to Queue, implemented by queues whose
// deliveries must be settled explicitly.
//
// A queue that redelivers unsettled messages — SQS, and any other at-least-once
// broker — implements this so a consumer can say whether it finished with a
// job. Queues that hand out a job exactly once, such as an in-memory channel,
// do not, and consumers must treat the interface as optional: assert for it and
// carry on without it when it is absent.
type Acknowledger interface {
	// Ack settles the delivery of jobID successfully, removing it from the
	// queue so it is not delivered again.
	Ack(ctx context.Context, jobID string) error
	// Nack abandons the delivery of jobID, returning it to the queue for
	// redelivery.
	Nack(ctx context.Context, jobID string) error
}

// Delivery is one job handed to a consumer, together with whatever metadata
// traveled alongside it.
type Delivery struct {
	// JobID identifies the job to process.
	JobID string
	// Meta holds the small string pairs attached when the job was enqueued:
	// the W3C trace context, and nothing else so far. It is nil when the
	// message carried none, which is the normal case for a job enqueued by
	// something that was not tracing.
	Meta map[string]string
}

// DeliveryConsumer is an optional companion to Queue, implemented by queues
// that can carry metadata beside the job identifier.
//
// It exists so a trace can survive the queue. The Queue port moves an
// identifier and nothing else, which is the right shape for the use cases —
// they have no business knowing a job arrived with a trace attached. A
// consumer that does care asserts for this interface and falls back to Consume
// when it is absent, exactly as it does for Acknowledger.
type DeliveryConsumer interface {
	// ConsumeDeliveries behaves as Queue.Consume, but yields the metadata that
	// arrived with each job as well as its identifier.
	ConsumeDeliveries(ctx context.Context) (<-chan Delivery, error)
}

// DepthReporter is an optional companion to Queue, implemented by queues that
// can say how much work is waiting.
//
// The figure is advisory: on a distributed broker it is an approximation that
// is already stale by the time it is read. It is meant for a metric or a
// dashboard, never for a decision about a specific job.
type DepthReporter interface {
	// Depth reports roughly how many jobs are waiting to be consumed.
	Depth(ctx context.Context) (int, error)
}

// JobRepository persists job state.
type JobRepository interface {
	// Save stores job, creating it or replacing it wholesale.
	Save(ctx context.Context, job *domain.Job) error
	// Get loads the job with the given id, returning ErrJobNotFound when it
	// does not exist.
	Get(ctx context.Context, id string) (*domain.Job, error)
	// UpdateStatus transitions the job to status. resultKey is recorded when
	// non-empty, and procErr is recorded as the failure reason when non-nil.
	UpdateStatus(ctx context.Context, id string, status domain.JobStatus, resultKey string, procErr error) error
}

// ImageProcessor applies a transformation to an encoded image.
type ImageProcessor interface {
	// Process reads the source image from input and returns the transformed
	// image encoded according to spec.
	Process(ctx context.Context, input io.Reader, spec domain.TransformationSpec) (io.Reader, error)
}
