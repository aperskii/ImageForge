// Package memrepo implements the ports.JobRepository port with an in-process
// map.
//
// It lets the API run with no database, for development and tests. Nothing is
// persisted: all job state is lost when the process exits.
package memrepo

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"imageforge/internal/domain"
	"imageforge/internal/ports"
)

// Compile-time assertion that JobRepository satisfies the port.
var _ ports.JobRepository = (*JobRepository)(nil)

// JobRepository stores jobs in a map guarded by a mutex.
//
// Jobs are copied in and out, so a caller mutating the job it passed to Save,
// or the one it got from Get, cannot corrupt the stored state.
type JobRepository struct {
	mu   sync.RWMutex
	jobs map[string]domain.Job
	now  func() time.Time
}

// Option overrides a JobRepository setting.
type Option func(*JobRepository)

// WithClock replaces the clock used to stamp UpdatedAt, for deterministic
// tests.
func WithClock(now func() time.Time) Option {
	return func(r *JobRepository) {
		if now != nil {
			r.now = now
		}
	}
}

// New returns an empty repository.
func New(opts ...Option) *JobRepository {
	repo := &JobRepository{
		jobs: make(map[string]domain.Job),
		now:  time.Now,
	}
	for _, opt := range opts {
		opt(repo)
	}
	return repo
}

// Save stores job, creating it or replacing it wholesale.
func (r *JobRepository) Save(ctx context.Context, job *domain.Job) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if job == nil {
		return errors.New("memrepo: save: nil job")
	}
	if job.ID == "" {
		return errors.New("memrepo: save: empty job id")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.jobs[job.ID] = *job
	return nil
}

// Get loads the job with the given id, returning ports.ErrJobNotFound when it
// does not exist.
func (r *JobRepository) Get(ctx context.Context, id string) (*domain.Job, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	job, ok := r.jobs[id]
	if !ok {
		return nil, fmt.Errorf("memrepo: get %s: %w", id, ports.ErrJobNotFound)
	}
	return &job, nil
}

// UpdateStatus transitions the job to status.
//
// resultKey is recorded when non-empty, and procErr is recorded as the failure
// reason when non-nil; a nil procErr clears any previous one. UpdatedAt is
// stamped on every call.
func (r *JobRepository) UpdateStatus(
	ctx context.Context,
	id string,
	status domain.JobStatus,
	resultKey string,
	procErr error,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !status.Valid() {
		return fmt.Errorf("memrepo: update %s: invalid status %q", id, status)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	job, ok := r.jobs[id]
	if !ok {
		return fmt.Errorf("memrepo: update %s: %w", id, ports.ErrJobNotFound)
	}

	job.Status = status
	job.UpdatedAt = r.now().UTC()
	if resultKey != "" {
		job.ResultKey = resultKey
	}
	if procErr != nil {
		job.Error = procErr.Error()
	} else {
		job.Error = ""
	}

	r.jobs[id] = job
	return nil
}

// Len reports how many jobs are stored. It is intended for tests and
// diagnostics.
func (r *JobRepository) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.jobs)
}
