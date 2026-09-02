package usecase

import (
	"context"
	"errors"
	"fmt"

	"imageforge/internal/domain"
	"imageforge/internal/ports"
)

// ErrJobNotPending is returned when a job is picked up while it is not in
// domain.StatusPending, which happens on duplicate delivery from an
// at-least-once queue or when another worker already owns the job.
var ErrJobNotPending = errors.New("job is not pending")

// ProcessJob transforms the source image of a pending job and records the
// outcome.
type ProcessJob struct {
	storage   ports.Storage
	jobs      ports.JobRepository
	processor ports.ImageProcessor
}

// NewProcessJob wires the use case to its dependencies.
func NewProcessJob(storage ports.Storage, jobs ports.JobRepository, processor ports.ImageProcessor) *ProcessJob {
	return &ProcessJob{storage: storage, jobs: jobs, processor: processor}
}

// Execute runs the transformation for jobID and returns the updated job.
//
// The job is moved to domain.StatusProcessing before any work starts, and to
// domain.StatusDone or domain.StatusFailed once the outcome is known.
func (uc *ProcessJob) Execute(ctx context.Context, jobID string) (*domain.Job, error) {
	job, err := uc.jobs.Get(ctx, jobID)
	if err != nil {
		return nil, fmt.Errorf("process job %s: load: %w", jobID, err)
	}
	if job.Status != domain.StatusPending {
		return job, fmt.Errorf("process job %s: %w: %s", jobID, ErrJobNotPending, job.Status)
	}

	if err = uc.jobs.UpdateStatus(ctx, jobID, domain.StatusProcessing, "", nil); err != nil {
		return nil, fmt.Errorf("process job %s: claim: %w", jobID, err)
	}
	job.Status = domain.StatusProcessing

	resultKey, err := uc.transform(ctx, job)
	if err != nil {
		return uc.fail(ctx, job, err)
	}

	if err = uc.jobs.UpdateStatus(ctx, jobID, domain.StatusDone, resultKey, nil); err != nil {
		return nil, fmt.Errorf("process job %s: mark done: %w", jobID, err)
	}

	job.Status = domain.StatusDone
	job.ResultKey = resultKey
	job.Error = ""
	return job, nil
}

// transform reads the source image, applies the job transformation and stores
// the result, returning the key it was written to.
func (uc *ProcessJob) transform(ctx context.Context, job *domain.Job) (string, error) {
	src, err := uc.storage.Get(ctx, job.OriginalKey)
	if err != nil {
		return "", fmt.Errorf("read original %s: %w", job.OriginalKey, err)
	}
	defer func() { _ = src.Close() }()

	out, err := uc.processor.Process(ctx, src, job.Transformation)
	if err != nil {
		return "", fmt.Errorf("transform: %w", err)
	}

	resultKey := ResultKey(job.ID, job.Transformation)
	if err = uc.storage.Put(ctx, resultKey, out); err != nil {
		return "", fmt.Errorf("store result %s: %w", resultKey, err)
	}
	return resultKey, nil
}

// fail records procErr against the job and returns it, joined with any error
// raised while recording it.
func (uc *ProcessJob) fail(ctx context.Context, job *domain.Job, procErr error) (*domain.Job, error) {
	wrapped := fmt.Errorf("process job %s: %w", job.ID, procErr)
	if err := uc.jobs.UpdateStatus(ctx, job.ID, domain.StatusFailed, "", wrapped); err != nil {
		return nil, errors.Join(wrapped, fmt.Errorf("mark failed: %w", err))
	}

	job.Status = domain.StatusFailed
	job.Error = wrapped.Error()
	return job, wrapped
}
