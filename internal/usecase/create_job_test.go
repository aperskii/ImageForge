package usecase

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"imageforge/internal/domain"
	"imageforge/internal/ports/mocks"
)

const (
	testJobID       = "0123456789abcdef0123456789abcdef"
	testOriginalKey = "originals/" + testJobID
	testResultKey   = "results/" + testJobID + ".webp"
)

var (
	testNow = time.Date(2026, time.September, 2, 10, 30, 0, 0, time.UTC)

	testSpec = domain.TransformationSpec{
		Width:         800,
		Height:        600,
		Format:        domain.FormatWebP,
		Quality:       82,
		StripMetadata: true,
	}

	errBoom   = errors.New("boom")
	errUpdate = errors.New("repository unavailable")
)

func TestCreateJobExecute(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		source    io.Reader
		spec      domain.TransformationSpec
		setup     func(storage *mocks.Storage, jobs *mocks.JobRepository, queue *mocks.Queue)
		wantErrs  []error
		assertJob func(t *testing.T, job *domain.Job)
	}{
		{
			name:   "persists and enqueues a pending job",
			source: strings.NewReader("source-bytes"),
			spec:   testSpec,
			setup: func(storage *mocks.Storage, jobs *mocks.JobRepository, queue *mocks.Queue) {
				storage.On("Put", mock.Anything, testOriginalKey, mock.Anything).Return(nil).Once()
				jobs.On("Save", mock.Anything, mock.AnythingOfType("*domain.Job")).Return(nil).Once()
				queue.On("Enqueue", mock.Anything, testJobID).Return(nil).Once()
			},
			assertJob: func(t *testing.T, job *domain.Job) {
				t.Helper()
				assert.Equal(t, testJobID, job.ID)
				assert.Equal(t, testOriginalKey, job.OriginalKey)
				assert.Empty(t, job.ResultKey, "the result key is unknown until processing succeeds")
				assert.Equal(t, domain.StatusPending, job.Status)
				assert.Equal(t, testSpec, job.Transformation)
				assert.Equal(t, testNow, job.CreatedAt)
				assert.Equal(t, testNow, job.UpdatedAt)
				assert.Empty(t, job.Error)
			},
		},
		{
			name:     "rejects a missing source",
			source:   nil,
			spec:     testSpec,
			wantErrs: []error{ErrNoSource},
		},
		{
			name:     "rejects an unsupported format before touching storage",
			source:   strings.NewReader("source-bytes"),
			spec:     domain.TransformationSpec{Width: 800, Format: domain.Format("gif"), Quality: 82},
			wantErrs: []error{domain.ErrInvalidFormat},
		},
		{
			name:     "rejects unset dimensions before touching storage",
			source:   strings.NewReader("source-bytes"),
			spec:     domain.TransformationSpec{Format: domain.FormatPNG},
			wantErrs: []error{domain.ErrInvalidDimensions},
		},
		{
			name:   "does not persist a job when the upload fails",
			source: strings.NewReader("source-bytes"),
			spec:   testSpec,
			setup: func(storage *mocks.Storage, _ *mocks.JobRepository, _ *mocks.Queue) {
				storage.On("Put", mock.Anything, testOriginalKey, mock.Anything).Return(errBoom).Once()
			},
			wantErrs: []error{errBoom},
		},
		{
			name:   "does not enqueue when persisting fails",
			source: strings.NewReader("source-bytes"),
			spec:   testSpec,
			setup: func(storage *mocks.Storage, jobs *mocks.JobRepository, _ *mocks.Queue) {
				storage.On("Put", mock.Anything, testOriginalKey, mock.Anything).Return(nil).Once()
				jobs.On("Save", mock.Anything, mock.AnythingOfType("*domain.Job")).Return(errBoom).Once()
			},
			wantErrs: []error{errBoom},
		},
		{
			name:   "marks the job failed when enqueueing fails",
			source: strings.NewReader("source-bytes"),
			spec:   testSpec,
			setup: func(storage *mocks.Storage, jobs *mocks.JobRepository, queue *mocks.Queue) {
				storage.On("Put", mock.Anything, testOriginalKey, mock.Anything).Return(nil).Once()
				jobs.On("Save", mock.Anything, mock.AnythingOfType("*domain.Job")).Return(nil).Once()
				queue.On("Enqueue", mock.Anything, testJobID).Return(errBoom).Once()
				jobs.On("UpdateStatus", mock.Anything, testJobID, domain.StatusFailed, "", mock.Anything).
					Return(nil).Once()
			},
			wantErrs: []error{errBoom},
		},
		{
			name:   "reports both errors when marking the failed job also fails",
			source: strings.NewReader("source-bytes"),
			spec:   testSpec,
			setup: func(storage *mocks.Storage, jobs *mocks.JobRepository, queue *mocks.Queue) {
				storage.On("Put", mock.Anything, testOriginalKey, mock.Anything).Return(nil).Once()
				jobs.On("Save", mock.Anything, mock.AnythingOfType("*domain.Job")).Return(nil).Once()
				queue.On("Enqueue", mock.Anything, testJobID).Return(errBoom).Once()
				jobs.On("UpdateStatus", mock.Anything, testJobID, domain.StatusFailed, "", mock.Anything).
					Return(errUpdate).Once()
			},
			wantErrs: []error{errBoom, errUpdate},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			storage := &mocks.Storage{}
			jobs := &mocks.JobRepository{}
			queue := &mocks.Queue{}
			if tt.setup != nil {
				tt.setup(storage, jobs, queue)
			}
			t.Cleanup(func() {
				storage.AssertExpectations(t)
				jobs.AssertExpectations(t)
				queue.AssertExpectations(t)
			})

			uc := newTestCreateJob(storage, jobs, queue)

			job, err := uc.Execute(context.Background(), CreateJobInput{Source: tt.source, Spec: tt.spec})

			if len(tt.wantErrs) > 0 {
				require.Error(t, err)
				assert.Nil(t, job)
				for _, want := range tt.wantErrs {
					assert.ErrorIs(t, err, want)
				}
				return
			}

			require.NoError(t, err)
			require.NotNil(t, job)
			tt.assertJob(t, job)
		})
	}
}

// TestCreateJobExecuteUploadsTheSourceUnmodified asserts the exact bytes handed
// to the use case are the ones that reach storage.
func TestCreateJobExecuteUploadsTheSourceUnmodified(t *testing.T) {
	t.Parallel()

	const payload = "the-original-image-bytes"

	storage := &mocks.Storage{}
	jobs := &mocks.JobRepository{}
	queue := &mocks.Queue{}

	var uploaded []byte
	storage.On("Put", mock.Anything, testOriginalKey, mock.Anything).
		Run(func(args mock.Arguments) {
			data, err := io.ReadAll(args.Get(2).(io.Reader))
			require.NoError(t, err)
			uploaded = data
		}).
		Return(nil).Once()
	jobs.On("Save", mock.Anything, mock.AnythingOfType("*domain.Job")).Return(nil).Once()
	queue.On("Enqueue", mock.Anything, testJobID).Return(nil).Once()

	uc := newTestCreateJob(storage, jobs, queue)

	_, err := uc.Execute(context.Background(), CreateJobInput{
		Source: strings.NewReader(payload),
		Spec:   testSpec,
	})

	require.NoError(t, err)
	assert.Equal(t, payload, string(uploaded))
	storage.AssertExpectations(t)
	jobs.AssertExpectations(t)
	queue.AssertExpectations(t)
}

// TestCreateJobExecuteSavesTheReturnedJob asserts the job handed to the
// repository is the very job returned to the caller, with UTC timestamps.
func TestCreateJobExecuteSavesTheReturnedJob(t *testing.T) {
	t.Parallel()

	storage := &mocks.Storage{}
	jobs := &mocks.JobRepository{}
	queue := &mocks.Queue{}

	var saved *domain.Job
	storage.On("Put", mock.Anything, testOriginalKey, mock.Anything).Return(nil).Once()
	jobs.On("Save", mock.Anything, mock.AnythingOfType("*domain.Job")).
		Run(func(args mock.Arguments) { saved = args.Get(1).(*domain.Job) }).
		Return(nil).Once()
	queue.On("Enqueue", mock.Anything, testJobID).Return(nil).Once()

	uc := NewCreateJob(storage, jobs, queue,
		WithIDFunc(func() string { return testJobID }),
		WithClock(func() time.Time { return testNow.In(time.FixedZone("CEST", 2*60*60)) }),
	)

	job, err := uc.Execute(context.Background(), CreateJobInput{
		Source: strings.NewReader("source-bytes"),
		Spec:   testSpec,
	})

	require.NoError(t, err)
	require.NotNil(t, saved)
	assert.Same(t, saved, job)
	assert.Equal(t, time.UTC, job.CreatedAt.Location(), "timestamps are normalized to UTC")
	assert.Equal(t, testNow, job.CreatedAt)

	storage.AssertExpectations(t)
	jobs.AssertExpectations(t)
	queue.AssertExpectations(t)
}

// TestNewCreateJobInstallsDefaults asserts the constructor provides a working
// identifier generator and clock when no option overrides them.
func TestNewCreateJobInstallsDefaults(t *testing.T) {
	t.Parallel()

	uc := NewCreateJob(&mocks.Storage{}, &mocks.JobRepository{}, &mocks.Queue{})

	require.NotNil(t, uc.newID)
	require.NotNil(t, uc.now)
	assert.Len(t, uc.newID(), 32, "identifiers are 128 bits in hexadecimal form")
	assert.NotEqual(t, uc.newID(), uc.newID(), "identifiers are unique")
	assert.WithinDuration(t, time.Now(), uc.now(), time.Minute)
}

func newTestCreateJob(storage *mocks.Storage, jobs *mocks.JobRepository, queue *mocks.Queue) *CreateJob {
	return NewCreateJob(storage, jobs, queue,
		WithIDFunc(func() string { return testJobID }),
		WithClock(func() time.Time { return testNow }),
	)
}
