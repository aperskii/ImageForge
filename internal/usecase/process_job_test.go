package usecase

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"imageforge/internal/domain"
	"imageforge/internal/ports"
	"imageforge/internal/ports/mocks"
)

func TestProcessJobExecute(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		setup      func(storage *mocks.Storage, jobs *mocks.JobRepository, processor *mocks.ImageProcessor)
		wantErrs   []error
		wantNilJob bool
		assertJob  func(t *testing.T, job *domain.Job)
	}{
		{
			name: "transforms the source and marks the job done",
			setup: func(storage *mocks.Storage, jobs *mocks.JobRepository, processor *mocks.ImageProcessor) {
				jobs.On("Get", mock.Anything, testJobID).Return(pendingJob(), nil).Once()
				jobs.On("UpdateStatus", mock.Anything, testJobID, domain.StatusProcessing, "", nil).
					Return(nil).Once()
				storage.On("Get", mock.Anything, testOriginalKey).
					Return(mocks.NewReadCloser("source-bytes"), nil).Once()
				processor.On("Process", mock.Anything, mock.Anything, testSpec).
					Return(strings.NewReader("result-bytes"), nil).Once()
				storage.On("Put", mock.Anything, testResultKey, mock.Anything).Return(nil).Once()
				jobs.On("UpdateStatus", mock.Anything, testJobID, domain.StatusDone, testResultKey, nil).
					Return(nil).Once()
			},
			assertJob: func(t *testing.T, job *domain.Job) {
				t.Helper()
				assert.Equal(t, domain.StatusDone, job.Status)
				assert.Equal(t, testResultKey, job.ResultKey)
				assert.Empty(t, job.Error)
			},
		},
		{
			name: "propagates a missing job",
			setup: func(_ *mocks.Storage, jobs *mocks.JobRepository, _ *mocks.ImageProcessor) {
				jobs.On("Get", mock.Anything, testJobID).Return(nil, ports.ErrJobNotFound).Once()
			},
			wantErrs:   []error{ports.ErrJobNotFound},
			wantNilJob: true,
		},
		{
			name: "refuses a job that is already done, without reprocessing it",
			setup: func(_ *mocks.Storage, jobs *mocks.JobRepository, _ *mocks.ImageProcessor) {
				job := pendingJob()
				job.Status = domain.StatusDone
				job.ResultKey = testResultKey
				jobs.On("Get", mock.Anything, testJobID).Return(job, nil).Once()
			},
			wantErrs: []error{ErrJobNotPending},
			assertJob: func(t *testing.T, job *domain.Job) {
				t.Helper()
				assert.Equal(t, domain.StatusDone, job.Status)
			},
		},
		{
			name: "refuses a job another worker is already processing",
			setup: func(_ *mocks.Storage, jobs *mocks.JobRepository, _ *mocks.ImageProcessor) {
				job := pendingJob()
				job.Status = domain.StatusProcessing
				jobs.On("Get", mock.Anything, testJobID).Return(job, nil).Once()
			},
			wantErrs: []error{ErrJobNotPending},
			assertJob: func(t *testing.T, job *domain.Job) {
				t.Helper()
				assert.Equal(t, domain.StatusProcessing, job.Status)
			},
		},
		{
			name: "does not read the source when claiming the job fails",
			setup: func(_ *mocks.Storage, jobs *mocks.JobRepository, _ *mocks.ImageProcessor) {
				jobs.On("Get", mock.Anything, testJobID).Return(pendingJob(), nil).Once()
				jobs.On("UpdateStatus", mock.Anything, testJobID, domain.StatusProcessing, "", nil).
					Return(errUpdate).Once()
			},
			wantErrs:   []error{errUpdate},
			wantNilJob: true,
		},
		{
			name: "marks the job failed when the source cannot be read",
			setup: func(storage *mocks.Storage, jobs *mocks.JobRepository, _ *mocks.ImageProcessor) {
				jobs.On("Get", mock.Anything, testJobID).Return(pendingJob(), nil).Once()
				jobs.On("UpdateStatus", mock.Anything, testJobID, domain.StatusProcessing, "", nil).
					Return(nil).Once()
				storage.On("Get", mock.Anything, testOriginalKey).Return(nil, errBoom).Once()
				jobs.On("UpdateStatus", mock.Anything, testJobID, domain.StatusFailed, "", mock.Anything).
					Return(nil).Once()
			},
			wantErrs:  []error{errBoom},
			assertJob: assertFailedJob,
		},
		{
			name: "marks the job failed when the transformation fails",
			setup: func(storage *mocks.Storage, jobs *mocks.JobRepository, processor *mocks.ImageProcessor) {
				jobs.On("Get", mock.Anything, testJobID).Return(pendingJob(), nil).Once()
				jobs.On("UpdateStatus", mock.Anything, testJobID, domain.StatusProcessing, "", nil).
					Return(nil).Once()
				storage.On("Get", mock.Anything, testOriginalKey).
					Return(mocks.NewReadCloser("source-bytes"), nil).Once()
				processor.On("Process", mock.Anything, mock.Anything, testSpec).
					Return(nil, errBoom).Once()
				jobs.On("UpdateStatus", mock.Anything, testJobID, domain.StatusFailed, "", mock.Anything).
					Return(nil).Once()
			},
			wantErrs:  []error{errBoom},
			assertJob: assertFailedJob,
		},
		{
			name: "marks the job failed when the result cannot be stored",
			setup: func(storage *mocks.Storage, jobs *mocks.JobRepository, processor *mocks.ImageProcessor) {
				jobs.On("Get", mock.Anything, testJobID).Return(pendingJob(), nil).Once()
				jobs.On("UpdateStatus", mock.Anything, testJobID, domain.StatusProcessing, "", nil).
					Return(nil).Once()
				storage.On("Get", mock.Anything, testOriginalKey).
					Return(mocks.NewReadCloser("source-bytes"), nil).Once()
				processor.On("Process", mock.Anything, mock.Anything, testSpec).
					Return(strings.NewReader("result-bytes"), nil).Once()
				storage.On("Put", mock.Anything, testResultKey, mock.Anything).Return(errBoom).Once()
				jobs.On("UpdateStatus", mock.Anything, testJobID, domain.StatusFailed, "", mock.Anything).
					Return(nil).Once()
			},
			wantErrs:  []error{errBoom},
			assertJob: assertFailedJob,
		},
		{
			name: "reports both errors when marking the failed job also fails",
			setup: func(storage *mocks.Storage, jobs *mocks.JobRepository, processor *mocks.ImageProcessor) {
				jobs.On("Get", mock.Anything, testJobID).Return(pendingJob(), nil).Once()
				jobs.On("UpdateStatus", mock.Anything, testJobID, domain.StatusProcessing, "", nil).
					Return(nil).Once()
				storage.On("Get", mock.Anything, testOriginalKey).
					Return(mocks.NewReadCloser("source-bytes"), nil).Once()
				processor.On("Process", mock.Anything, mock.Anything, testSpec).
					Return(nil, errBoom).Once()
				jobs.On("UpdateStatus", mock.Anything, testJobID, domain.StatusFailed, "", mock.Anything).
					Return(errUpdate).Once()
			},
			wantErrs:   []error{errBoom, errUpdate},
			wantNilJob: true,
		},
		{
			name: "propagates a failure to record the successful outcome",
			setup: func(storage *mocks.Storage, jobs *mocks.JobRepository, processor *mocks.ImageProcessor) {
				jobs.On("Get", mock.Anything, testJobID).Return(pendingJob(), nil).Once()
				jobs.On("UpdateStatus", mock.Anything, testJobID, domain.StatusProcessing, "", nil).
					Return(nil).Once()
				storage.On("Get", mock.Anything, testOriginalKey).
					Return(mocks.NewReadCloser("source-bytes"), nil).Once()
				processor.On("Process", mock.Anything, mock.Anything, testSpec).
					Return(strings.NewReader("result-bytes"), nil).Once()
				storage.On("Put", mock.Anything, testResultKey, mock.Anything).Return(nil).Once()
				jobs.On("UpdateStatus", mock.Anything, testJobID, domain.StatusDone, testResultKey, nil).
					Return(errUpdate).Once()
			},
			wantErrs:   []error{errUpdate},
			wantNilJob: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			storage := &mocks.Storage{}
			jobs := &mocks.JobRepository{}
			processor := &mocks.ImageProcessor{}
			tt.setup(storage, jobs, processor)
			t.Cleanup(func() {
				storage.AssertExpectations(t)
				jobs.AssertExpectations(t)
				processor.AssertExpectations(t)
			})

			uc := NewProcessJob(storage, jobs, processor)

			job, err := uc.Execute(context.Background(), testJobID)

			if len(tt.wantErrs) > 0 {
				require.Error(t, err)
				for _, want := range tt.wantErrs {
					assert.ErrorIs(t, err, want)
				}
			} else {
				require.NoError(t, err)
			}

			if tt.wantNilJob {
				assert.Nil(t, job)
				return
			}

			require.NotNil(t, job)
			tt.assertJob(t, job)
		})
	}
}

// TestProcessJobExecuteClosesTheSource asserts the source reader is released
// whether the transformation succeeds or fails.
func TestProcessJobExecuteClosesTheSource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		processErr  error
		expectExtra func(storage *mocks.Storage, jobs *mocks.JobRepository)
	}{
		{
			name: "on success",
			expectExtra: func(storage *mocks.Storage, jobs *mocks.JobRepository) {
				storage.On("Put", mock.Anything, testResultKey, mock.Anything).Return(nil).Once()
				jobs.On("UpdateStatus", mock.Anything, testJobID, domain.StatusDone, testResultKey, nil).
					Return(nil).Once()
			},
		},
		{
			name:       "on failure",
			processErr: errBoom,
			expectExtra: func(_ *mocks.Storage, jobs *mocks.JobRepository) {
				jobs.On("UpdateStatus", mock.Anything, testJobID, domain.StatusFailed, "", mock.Anything).
					Return(nil).Once()
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			storage := &mocks.Storage{}
			jobs := &mocks.JobRepository{}
			processor := &mocks.ImageProcessor{}
			source := mocks.NewReadCloser("source-bytes")

			jobs.On("Get", mock.Anything, testJobID).Return(pendingJob(), nil).Once()
			jobs.On("UpdateStatus", mock.Anything, testJobID, domain.StatusProcessing, "", nil).
				Return(nil).Once()
			storage.On("Get", mock.Anything, testOriginalKey).Return(source, nil).Once()

			var out *strings.Reader
			if tt.processErr == nil {
				out = strings.NewReader("result-bytes")
			}
			processor.On("Process", mock.Anything, mock.Anything, testSpec).
				Return(readerOrNil(out), tt.processErr).Once()
			tt.expectExtra(storage, jobs)

			uc := NewProcessJob(storage, jobs, processor)

			_, err := uc.Execute(context.Background(), testJobID)

			if tt.processErr != nil {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.True(t, source.Closed, "the source reader must always be closed")

			storage.AssertExpectations(t)
			jobs.AssertExpectations(t)
			processor.AssertExpectations(t)
		})
	}
}

// TestProcessJobExecuteStoresTheProcessorOutput asserts the bytes produced by
// the processor are the bytes written to storage, under the result key derived
// from the requested output format.
func TestProcessJobExecuteStoresTheProcessorOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		format        domain.Format
		quality       int
		wantResultKey string
	}{
		{name: "webp", format: domain.FormatWebP, quality: 82, wantResultKey: "results/" + testJobID + ".webp"},
		{name: "jpeg", format: domain.FormatJPEG, quality: 90, wantResultKey: "results/" + testJobID + ".jpg"},
		{name: "png", format: domain.FormatPNG, wantResultKey: "results/" + testJobID + ".png"},
		{name: "avif", format: domain.FormatAVIF, quality: 45, wantResultKey: "results/" + testJobID + ".avif"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			const payload = "the-transformed-image-bytes"

			spec := domain.TransformationSpec{
				Width:   800,
				Height:  600,
				Format:  tt.format,
				Quality: tt.quality,
			}
			job := pendingJob()
			job.Transformation = spec

			storage := &mocks.Storage{}
			jobs := &mocks.JobRepository{}
			processor := &mocks.ImageProcessor{}

			var stored []byte
			jobs.On("Get", mock.Anything, testJobID).Return(job, nil).Once()
			jobs.On("UpdateStatus", mock.Anything, testJobID, domain.StatusProcessing, "", nil).
				Return(nil).Once()
			storage.On("Get", mock.Anything, testOriginalKey).
				Return(mocks.NewReadCloser("source-bytes"), nil).Once()
			processor.On("Process", mock.Anything, mock.Anything, spec).
				Return(strings.NewReader(payload), nil).Once()
			storage.On("Put", mock.Anything, tt.wantResultKey, mock.Anything).
				Run(func(args mock.Arguments) {
					data, err := io.ReadAll(args.Get(2).(io.Reader))
					require.NoError(t, err)
					stored = data
				}).
				Return(nil).Once()
			jobs.On("UpdateStatus", mock.Anything, testJobID, domain.StatusDone, tt.wantResultKey, nil).
				Return(nil).Once()

			uc := NewProcessJob(storage, jobs, processor)

			got, err := uc.Execute(context.Background(), testJobID)

			require.NoError(t, err)
			assert.Equal(t, payload, string(stored))
			assert.Equal(t, tt.wantResultKey, got.ResultKey)

			storage.AssertExpectations(t)
			jobs.AssertExpectations(t)
			processor.AssertExpectations(t)
		})
	}
}

func TestResultKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		spec domain.TransformationSpec
		want string
	}{
		{name: "jpeg", spec: domain.TransformationSpec{Format: domain.FormatJPEG}, want: "results/id.jpg"},
		{name: "png", spec: domain.TransformationSpec{Format: domain.FormatPNG}, want: "results/id.png"},
		{name: "webp", spec: domain.TransformationSpec{Format: domain.FormatWebP}, want: "results/id.webp"},
		{name: "avif", spec: domain.TransformationSpec{Format: domain.FormatAVIF}, want: "results/id.avif"},
		{name: "unknown format has no extension", spec: domain.TransformationSpec{}, want: "results/id"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, ResultKey("id", tt.spec))
		})
	}
}

func TestOriginalKey(t *testing.T) {
	t.Parallel()
	assert.Equal(t, testOriginalKey, OriginalKey(testJobID))
}

// readerOrNil returns an untyped nil when r is nil, so that the mock hands the
// use case a genuinely nil io.Reader rather than an interface wrapping a typed
// nil pointer.
func readerOrNil(r *strings.Reader) any {
	if r == nil {
		return nil
	}
	return r
}

func pendingJob() *domain.Job {
	return &domain.Job{
		ID:             testJobID,
		OriginalKey:    testOriginalKey,
		Status:         domain.StatusPending,
		Transformation: testSpec,
		CreatedAt:      testNow,
		UpdatedAt:      testNow,
	}
}

func assertFailedJob(t *testing.T, job *domain.Job) {
	t.Helper()
	assert.Equal(t, domain.StatusFailed, job.Status)
	assert.Empty(t, job.ResultKey)
	assert.Contains(t, job.Error, errBoom.Error())
}
