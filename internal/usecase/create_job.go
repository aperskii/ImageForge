package usecase

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"imageforge/internal/domain"
	"imageforge/internal/ports"
)

// ErrNoSource is returned when a job is created without a source image.
var ErrNoSource = errors.New("no source image provided")

// CreateJobInput is the request accepted by CreateJob.
type CreateJobInput struct {
	// Source is the encoded image to transform.
	Source io.Reader
	// Spec describes the transformation to apply.
	Spec domain.TransformationSpec
}

// CreateJob stores an uploaded image, records a pending job for it and
// publishes that job for asynchronous processing.
type CreateJob struct {
	storage ports.Storage
	jobs    ports.JobRepository
	queue   ports.Queue
	newID   IDFunc
	now     Clock
}

// CreateJobOption overrides a CreateJob dependency.
type CreateJobOption func(*CreateJob)

// WithIDFunc replaces the identifier generator, mainly for deterministic tests.
func WithIDFunc(fn IDFunc) CreateJobOption {
	return func(uc *CreateJob) { uc.newID = fn }
}

// WithClock replaces the clock, mainly for deterministic tests.
func WithClock(c Clock) CreateJobOption {
	return func(uc *CreateJob) { uc.now = c }
}

// NewCreateJob wires the use case to its dependencies.
func NewCreateJob(storage ports.Storage, jobs ports.JobRepository, queue ports.Queue, opts ...CreateJobOption) *CreateJob {
	uc := &CreateJob{
		storage: storage,
		jobs:    jobs,
		queue:   queue,
		newID:   newID,
		now:     time.Now,
	}
	for _, opt := range opts {
		opt(uc)
	}
	return uc
}

// Execute validates the request, uploads the source image, persists a pending
// job and enqueues it. The returned job is the state that was persisted.
//
// If enqueueing fails the job is marked failed, so that it is not left pending
// forever with no worker to pick it up.
func (uc *CreateJob) Execute(ctx context.Context, in CreateJobInput) (*domain.Job, error) {
	if in.Source == nil {
		return nil, ErrNoSource
	}
	if err := in.Spec.Validate(); err != nil {
		return nil, fmt.Errorf("create job: invalid transformation: %w", err)
	}

	id := uc.newID()
	originalKey := OriginalKey(id)

	if err := uc.storage.Put(ctx, originalKey, in.Source); err != nil {
		return nil, fmt.Errorf("create job %s: store original: %w", id, err)
	}

	now := uc.now().UTC()
	job := &domain.Job{
		ID:             id,
		OriginalKey:    originalKey,
		Status:         domain.StatusPending,
		Transformation: in.Spec,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := uc.jobs.Save(ctx, job); err != nil {
		return nil, fmt.Errorf("create job %s: save: %w", id, err)
	}

	if err := uc.queue.Enqueue(ctx, id); err != nil {
		enqueueErr := fmt.Errorf("create job %s: enqueue: %w", id, err)
		if markErr := uc.jobs.UpdateStatus(ctx, id, domain.StatusFailed, "", enqueueErr); markErr != nil {
			return nil, errors.Join(enqueueErr, fmt.Errorf("mark failed: %w", markErr))
		}
		return nil, enqueueErr
	}

	return job, nil
}
