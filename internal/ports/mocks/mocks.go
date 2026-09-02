// Package mocks provides testify-based test doubles for every interface
// declared in internal/ports. They are intended for unit tests of the use
// cases and adapters.
package mocks

import (
	"context"
	"io"

	"github.com/stretchr/testify/mock"

	"imageforge/internal/domain"
	"imageforge/internal/ports"
)

// Compile-time assertions that every mock satisfies its port.
var (
	_ ports.Storage        = (*Storage)(nil)
	_ ports.Queue          = (*Queue)(nil)
	_ ports.JobRepository  = (*JobRepository)(nil)
	_ ports.ImageProcessor = (*ImageProcessor)(nil)
)

// Storage is a mock implementation of ports.Storage.
type Storage struct {
	mock.Mock
}

// Put records the call and returns the configured error.
func (m *Storage) Put(ctx context.Context, key string, data io.Reader) error {
	return m.Called(ctx, key, data).Error(0)
}

// Get records the call and returns the configured reader and error.
func (m *Storage) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	args := m.Called(ctx, key)
	rc, _ := args.Get(0).(io.ReadCloser)
	return rc, args.Error(1)
}

// Queue is a mock implementation of ports.Queue.
type Queue struct {
	mock.Mock
}

// Enqueue records the call and returns the configured error.
func (m *Queue) Enqueue(ctx context.Context, jobID string) error {
	return m.Called(ctx, jobID).Error(0)
}

// Consume records the call and returns the configured channel and error.
func (m *Queue) Consume(ctx context.Context) (<-chan string, error) {
	args := m.Called(ctx)
	ch, _ := args.Get(0).(<-chan string)
	return ch, args.Error(1)
}

// JobRepository is a mock implementation of ports.JobRepository.
type JobRepository struct {
	mock.Mock
}

// Save records the call and returns the configured error.
func (m *JobRepository) Save(ctx context.Context, job *domain.Job) error {
	return m.Called(ctx, job).Error(0)
}

// Get records the call and returns the configured job and error.
func (m *JobRepository) Get(ctx context.Context, id string) (*domain.Job, error) {
	args := m.Called(ctx, id)
	job, _ := args.Get(0).(*domain.Job)
	return job, args.Error(1)
}

// UpdateStatus records the call and returns the configured error.
func (m *JobRepository) UpdateStatus(ctx context.Context, id string, status domain.JobStatus, resultKey string, procErr error) error {
	return m.Called(ctx, id, status, resultKey, procErr).Error(0)
}

// ImageProcessor is a mock implementation of ports.ImageProcessor.
type ImageProcessor struct {
	mock.Mock
}

// Process records the call and returns the configured reader and error.
func (m *ImageProcessor) Process(ctx context.Context, input io.Reader, spec domain.TransformationSpec) (io.Reader, error) {
	args := m.Called(ctx, input, spec)
	r, _ := args.Get(0).(io.Reader)
	return r, args.Error(1)
}
