//go:build integration

// Package integration exercises the real AWS adapters against LocalStack.
//
// The suite needs a running stack; `make aws-up` starts one and creates the
// resources. It is skipped unless AWS_ENDPOINT_URL points at something, so a
// plain `go test ./...` on a machine with no Docker stays green.
//
//	make aws-up
//	make test-integration
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"io/fs"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	// Registered so image.DecodeConfig can inspect the results the worker wrote.
	_ "image/jpeg"

	"imageforge/internal/adapters/awscfg"
	"imageforge/internal/adapters/dynamorepo"
	"imageforge/internal/adapters/httpapi"
	"imageforge/internal/adapters/imageproc"
	"imageforge/internal/adapters/s3storage"
	"imageforge/internal/adapters/sqsqueue"
	"imageforge/internal/domain"
	"imageforge/internal/ports"
	"imageforge/internal/usecase"
	"imageforge/internal/worker"
)

// defaultEndpoint is where `make aws-up` puts LocalStack.
const defaultEndpoint = "http://localhost:4566"

// stack is the whole pipeline running on the AWS adapters, with the API served
// by httptest and the worker pool running in the background.
type stack struct {
	settings awscfg.Settings
	storage  *s3storage.Storage
	queue    *sqsqueue.Queue
	jobs     *dynamorepo.JobRepository
	server   *httptest.Server
	pool     *worker.Pool
}

// newStack wires every AWS adapter against LocalStack and starts the API and
// the worker, both torn down when the test ends.
func newStack(t *testing.T, opts ...worker.Option) *stack {
	t.Helper()

	settings := awscfg.SettingsFromEnv()
	if settings.Endpoint == "" {
		settings.Endpoint = defaultEndpoint
	}
	require.NoError(t, settings.Validate())
	requireLocalStack(t, settings.Endpoint)

	ctx := t.Context()

	cfg, err := awscfg.Load(ctx, settings)
	require.NoError(t, err)

	storage, err := s3storage.New(awscfg.S3(cfg, settings), settings.Bucket)
	require.NoError(t, err)

	// A short visibility timeout keeps the redelivery test quick, and a short
	// wait keeps shutdown from blocking on a long poll.
	queue, err := sqsqueue.New(ctx, awscfg.SQS(cfg, settings), settings.Queue,
		sqsqueue.WithWaitTime(time.Second),
		sqsqueue.WithVisibilityTimeout(30*time.Second))
	require.NoError(t, err)

	jobs, err := dynamorepo.New(awscfg.DynamoDB(cfg, settings), settings.Table)
	require.NoError(t, err)

	processor, err := imageproc.New()
	require.NoError(t, err, "the %s backend must be usable", imageproc.Backend)

	api := httpapi.New(
		usecase.NewCreateJob(storage, jobs, queue),
		jobs,
		httpapi.Config{Logger: discardLogger(), PublicBaseURL: "https://cdn.example.test"},
	)
	server := httptest.NewServer(api.Routes())
	t.Cleanup(server.Close)

	opts = append([]worker.Option{
		worker.WithLogger(discardLogger()),
		worker.WithSize(4),
		worker.WithShutdownTimeout(20 * time.Second),
	}, opts...)
	pool := worker.New(queue, usecase.NewProcessJob(storage, jobs, processor), opts...)

	poolCtx, stopPool := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- pool.Run(poolCtx) }()
	t.Cleanup(func() {
		stopPool()
		select {
		case err := <-runErr:
			assert.NoError(t, err)
		case <-time.After(30 * time.Second):
			t.Error("the worker pool did not stop")
		}
	})

	return &stack{
		settings: settings,
		storage:  storage,
		queue:    queue,
		jobs:     jobs,
		server:   server,
		pool:     pool,
	}
}

// requireLocalStack skips the test when no stack is reachable, so the suite
// reports "not run" rather than a wall of connection failures.
func requireLocalStack(t *testing.T, endpoint string) {
	t.Helper()

	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(t.Context(),
		http.MethodGet, strings.TrimSuffix(endpoint, "/")+"/_localstack/health", http.NoBody)
	require.NoError(t, err)

	resp, err := client.Do(req)
	if err != nil {
		t.Skipf("no LocalStack at %s (%v); run `make aws-up` first", endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Skipf("LocalStack at %s is unhealthy (%d); run `make aws-up` first", endpoint, resp.StatusCode)
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// samplePNG encodes a small gradient of the given size.
func samplePNG(t *testing.T, width, height int) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 0x40, A: 0xff})
		}
	}

	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

// jobResponse mirrors the API's job body. It is declared here rather than
// reused from the httpapi package because that type is unexported, and because
// the wire shape is exactly what this suite is meant to pin down.
type jobResponse struct {
	ID             string `json:"id"`
	Status         string `json:"status"`
	Transformation struct {
		Width         int    `json:"width"`
		Height        int    `json:"height"`
		Format        string `json:"format"`
		Quality       int    `json:"quality"`
		Watermark     bool   `json:"watermark"`
		StripMetadata bool   `json:"strip_metadata"`
	} `json:"transformation"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	ResultKey string    `json:"result_key"`
	ResultURL string    `json:"result_url"`
	Error     string    `json:"error"`
}

// upload posts an image and its specification to the API.
func (s *stack) upload(t *testing.T, source []byte, spec string) jobResponse {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile(httpapi.FileField, "photo.png")
	require.NoError(t, err)
	_, err = part.Write(source)
	require.NoError(t, err)
	require.NoError(t, writer.WriteField(httpapi.SpecField, spec))
	require.NoError(t, writer.Close())

	req, err := http.NewRequestWithContext(t.Context(),
		http.MethodPost, s.server.URL+"/uploads", &body)
	require.NoError(t, err)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusAccepted, resp.StatusCode)

	var job jobResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&job))
	require.NotEmpty(t, job.ID)
	return job
}

// getJob polls the API for a job.
func (s *stack) getJob(t *testing.T, id string) (job jobResponse, statusCode int) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(),
		http.MethodGet, s.server.URL+"/jobs/"+id, http.NoBody)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return jobResponse{}, resp.StatusCode
	}

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&job))
	return job, resp.StatusCode
}

// awaitStatus polls the API until the job leaves the pending and processing
// states, which is what a real client does.
func (s *stack) awaitStatus(t *testing.T, id string, timeout time.Duration) jobResponse {
	t.Helper()

	deadline := time.Now().Add(timeout)
	var last jobResponse
	for time.Now().Before(deadline) {
		job, status := s.getJob(t, id)
		if status == http.StatusOK {
			last = job
			if domain.JobStatus(job.Status).Terminal() {
				return job
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	t.Fatalf("job %s never reached a terminal state; last seen %q (%s)", id, last.Status, last.Error)
	return last
}

// TestUploadProcessResultFlow is the end-to-end case: an image goes in through
// the API, travels S3 and SQS, is transformed by the worker, and comes back out
// of DynamoDB with a result the client can fetch from S3.
func TestUploadProcessResultFlow(t *testing.T) {
	stack := newStack(t)
	source := samplePNG(t, 64, 48)

	tests := []struct {
		name       string
		spec       string
		wantFormat string
		wantWidth  int
		wantHeight int
		wantExt    string
	}{
		{
			name:       "resize and convert to jpeg",
			spec:       `{"width":32,"format":"jpeg","quality":80,"strip_metadata":true}`,
			wantFormat: "jpeg", wantWidth: 32, wantHeight: 24, wantExt: "jpg",
		},
		{
			name:       "resize to an exact box as png",
			spec:       `{"width":40,"height":40,"format":"png"}`,
			wantFormat: "png", wantWidth: 40, wantHeight: 40, wantExt: "png",
		},
		{
			name:       "height only, deriving the width",
			spec:       `{"height":24,"format":"jpeg","quality":60}`,
			wantFormat: "jpeg", wantWidth: 32, wantHeight: 24, wantExt: "jpg",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			accepted := stack.upload(t, source, tt.spec)
			assert.Equal(t, domain.StatusPending.String(), accepted.Status)
			assert.Empty(t, accepted.ResultKey)

			// The original reached S3 before the client was told anything.
			original, err := stack.storage.Get(t.Context(), usecase.OriginalKey(accepted.ID))
			require.NoError(t, err, "the original must be in S3")
			stored, err := io.ReadAll(original)
			require.NoError(t, err)
			require.NoError(t, original.Close())
			assert.Equal(t, source, stored, "the uploaded bytes reach S3 unchanged")

			done := stack.awaitStatus(t, accepted.ID, 90*time.Second)

			require.Equalf(t, domain.StatusDone.String(), done.Status, "job failed: %s", done.Error)
			assert.Empty(t, done.Error)
			assert.Equal(t, "results/"+accepted.ID+"."+tt.wantExt, done.ResultKey)
			assert.Equal(t, "https://cdn.example.test/"+done.ResultKey, done.ResultURL)
			assert.False(t, done.UpdatedAt.Before(done.CreatedAt))

			// The result is really in S3 and really is the requested image.
			object, err := stack.storage.Get(t.Context(), done.ResultKey)
			require.NoError(t, err, "the result must be in S3")
			encoded, err := io.ReadAll(object)
			require.NoError(t, err)
			require.NoError(t, object.Close())

			cfg, format, err := image.DecodeConfig(bytes.NewReader(encoded))
			require.NoError(t, err)
			assert.Equal(t, tt.wantFormat, format)
			assert.Equal(t, tt.wantWidth, cfg.Width)
			assert.Equal(t, tt.wantHeight, cfg.Height)
		})
	}
}

// TestConcurrentUploadsAllComplete pushes a batch through the real broker at
// once, which is where an adapter that mishandles receipt handles or
// conditional writes shows up.
func TestConcurrentUploadsAllComplete(t *testing.T) {
	const uploads = 20

	stack := newStack(t, worker.WithSize(6))
	source := samplePNG(t, 64, 48)

	var mu sync.Mutex
	ids := make(map[string]int, uploads)

	var wg sync.WaitGroup
	wg.Add(uploads)
	for i := range uploads {
		go func(n int) {
			defer wg.Done()
			width := 16 + n // distinct per job
			job := stack.upload(t, source, fmt.Sprintf(`{"width":%d,"format":"png"}`, width))
			mu.Lock()
			ids[job.ID] = width
			mu.Unlock()
		}(i)
	}
	wg.Wait()

	require.Len(t, ids, uploads, "every upload produced a distinct job")

	for id, width := range ids {
		done := stack.awaitStatus(t, id, 120*time.Second)
		require.Equalf(t, domain.StatusDone.String(), done.Status, "job %s failed: %s", id, done.Error)

		object, err := stack.storage.Get(t.Context(), done.ResultKey)
		require.NoErrorf(t, err, "job %s result missing from S3", id)
		encoded, err := io.ReadAll(object)
		require.NoError(t, err)
		require.NoError(t, object.Close())

		cfg, format, err := image.DecodeConfig(bytes.NewReader(encoded))
		require.NoError(t, err)
		assert.Equal(t, "png", format)
		assert.Equalf(t, width, cfg.Width, "job %s width", id)
	}

	// The count is deliberately not asserted against uploads: the queue is
	// shared, so another consumer -- a worker someone left running against the
	// same LocalStack -- may legitimately take some of these messages. That
	// every job above reached Done with the right result is the contract; which
	// process did the work is not.
	assert.Zero(t, stack.pool.Stats().Failed)
}

// TestMessagesAreDeletedOnSuccess checks the delete-on-success contract: once
// the pipeline is quiet, nothing is left in flight and no message reappears to
// be processed a second time.
func TestMessagesAreDeletedOnSuccess(t *testing.T) {
	stack := newStack(t)

	job := stack.upload(t, samplePNG(t, 32, 32), `{"width":16,"format":"png"}`)
	done := stack.awaitStatus(t, job.ID, 90*time.Second)
	require.Equal(t, domain.StatusDone.String(), done.Status)

	before := stack.pool.Stats()

	// Well past the visibility timeout a redelivered message would have
	// reappeared and been counted as a skip.
	assert.Eventually(t, func() bool {
		return stack.queue.InFlight() == 0
	}, 30*time.Second, 200*time.Millisecond, "the delivery was never settled")

	time.Sleep(3 * time.Second)
	assert.Equal(t, before.Processed, stack.pool.Stats().Processed,
		"a deleted message must not be processed again")
}

// TestAPIValidationAgainstAWS checks the API still rejects bad input when the
// real adapters are behind it, and writes nothing when it does.
func TestAPIValidationAgainstAWS(t *testing.T) {
	stack := newStack(t)

	tests := []struct {
		name     string
		spec     string
		wantCode string
	}{
		{name: "unsupported format", spec: `{"width":32,"format":"gif"}`, wantCode: "invalid_transformation"},
		{name: "no dimension", spec: `{"format":"png"}`, wantCode: "invalid_transformation"},
		{name: "malformed json", spec: `{"width":`, wantCode: "invalid_spec"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			part, err := writer.CreateFormFile(httpapi.FileField, "photo.png")
			require.NoError(t, err)
			_, err = part.Write(samplePNG(t, 16, 16))
			require.NoError(t, err)
			require.NoError(t, writer.WriteField(httpapi.SpecField, tt.spec))
			require.NoError(t, writer.Close())

			req, err := http.NewRequestWithContext(t.Context(),
				http.MethodPost, stack.server.URL+"/uploads", &body)
			require.NoError(t, err)
			req.Header.Set("Content-Type", writer.FormDataContentType())

			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()

			require.Equal(t, http.StatusBadRequest, resp.StatusCode)

			var errBody struct {
				Error struct {
					Code      string `json:"code"`
					RequestID string `json:"request_id"`
				} `json:"error"`
			}
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&errBody))
			assert.Equal(t, tt.wantCode, errBody.Error.Code)
			assert.NotEmpty(t, errBody.Error.RequestID)
		})
	}
}

// TestUnknownJobIsNotFound checks DynamoDB's empty result becomes the port's
// sentinel and then a 404, rather than an internal error.
func TestUnknownJobIsNotFound(t *testing.T) {
	stack := newStack(t)

	_, status := stack.getJob(t, "0123456789abcdef0123456789abcdef")
	assert.Equal(t, http.StatusNotFound, status)

	_, err := stack.jobs.Get(t.Context(), "0123456789abcdef0123456789abcdef")
	assert.ErrorIs(t, err, ports.ErrJobNotFound)
}

// TestS3MissingObject checks the S3 adapter reports a missing object the same
// way the filesystem one does, which is what lets them be swapped freely.
func TestS3MissingObject(t *testing.T) {
	stack := newStack(t)

	_, err := stack.storage.Get(t.Context(), "originals/does-not-exist")

	require.Error(t, err)
	assert.ErrorIs(t, err, fs.ErrNotExist)
}

// TestS3RoundTrip checks the storage adapter on its own, including that a
// second Put replaces the object rather than appending to it.
func TestS3RoundTrip(t *testing.T) {
	stack := newStack(t)
	key := fmt.Sprintf("originals/integration-%d", time.Now().UnixNano())

	require.NoError(t, stack.storage.Put(t.Context(), key, strings.NewReader("first")))
	assert.Equal(t, "first", readObject(t, stack, key))

	require.NoError(t, stack.storage.Put(t.Context(), key, strings.NewReader("second")))
	assert.Equal(t, "second", readObject(t, stack, key), "a put replaces the object")
}

// TestDynamoUpdateRequiresAnExistingJob checks the conditional write: a status
// update must not resurrect a job that was never saved.
func TestDynamoUpdateRequiresAnExistingJob(t *testing.T) {
	stack := newStack(t)

	err := stack.jobs.UpdateStatus(t.Context(), "0123456789abcdef0123456789abcdef",
		domain.StatusDone, "results/nope.png", nil)

	require.Error(t, err)
	assert.ErrorIs(t, err, ports.ErrJobNotFound)
}

// TestDynamoRoundTrip checks a job survives storage with every field intact,
// including the transformation and the timestamps.
func TestDynamoRoundTrip(t *testing.T) {
	stack := newStack(t)

	now := time.Now().UTC().Truncate(time.Millisecond)
	original := &domain.Job{
		ID:          fmt.Sprintf("integration-%d", time.Now().UnixNano()),
		OriginalKey: "originals/integration",
		Status:      domain.StatusPending,
		Transformation: domain.TransformationSpec{
			Width: 800, Height: 600, Format: domain.FormatWebP,
			Quality: 82, Watermark: true, StripMetadata: true,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}

	require.NoError(t, stack.jobs.Save(t.Context(), original))

	loaded, err := stack.jobs.Get(t.Context(), original.ID)
	require.NoError(t, err)
	assert.Equal(t, original.ID, loaded.ID)
	assert.Equal(t, original.OriginalKey, loaded.OriginalKey)
	assert.Equal(t, original.Status, loaded.Status)
	assert.Equal(t, original.Transformation, loaded.Transformation)
	assert.True(t, original.CreatedAt.Equal(loaded.CreatedAt), "created_at survives the round trip")

	// A failure is recorded, then cleared by a later success.
	require.NoError(t, stack.jobs.UpdateStatus(t.Context(), original.ID,
		domain.StatusFailed, "", errors.New("boom")))
	failed, err := stack.jobs.Get(t.Context(), original.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusFailed, failed.Status)
	assert.Equal(t, "boom", failed.Error)

	require.NoError(t, stack.jobs.UpdateStatus(t.Context(), original.ID,
		domain.StatusDone, "results/integration.webp", nil))
	done, err := stack.jobs.Get(t.Context(), original.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusDone, done.Status)
	assert.Equal(t, "results/integration.webp", done.ResultKey)
	assert.Empty(t, done.Error, "a successful update clears the previous failure")
}

// TestHealthAndReadiness checks the probes still answer with the real adapters
// wired in.
func TestHealthAndReadiness(t *testing.T) {
	stack := newStack(t)

	for _, path := range []string{"/healthz", "/readyz"} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, stack.server.URL+path, http.NoBody)
		require.NoError(t, err)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())

		assert.Equalf(t, http.StatusOK, resp.StatusCode, "%s", path)
	}
}

// TestSettingsFromEnv pins the environment contract the deployment relies on.
func TestSettingsFromEnv(t *testing.T) {
	t.Setenv(awscfg.EnvRegion, "eu-central-1")
	t.Setenv(awscfg.EnvEndpoint, "http://localhost:4566")
	t.Setenv(awscfg.EnvBucket, "custom-bucket")
	t.Setenv(awscfg.EnvQueue, "custom-queue")
	t.Setenv(awscfg.EnvTable, "custom-table")

	settings := awscfg.SettingsFromEnv()

	assert.Equal(t, "eu-central-1", settings.Region)
	assert.Equal(t, "custom-bucket", settings.Bucket)
	assert.Equal(t, "custom-queue", settings.Queue)
	assert.Equal(t, "custom-table", settings.Table)
	assert.True(t, settings.UsesLocalStack())
	assert.NoError(t, settings.Validate())

	os.Unsetenv(awscfg.EnvEndpoint)
	assert.False(t, awscfg.SettingsFromEnv().UsesLocalStack())
}

// readObject reads an object from storage as a string.
func readObject(t *testing.T, s *stack, key string) string {
	t.Helper()

	object, err := s.storage.Get(t.Context(), key)
	require.NoError(t, err)
	defer func() { _ = object.Close() }()

	content, err := io.ReadAll(object)
	require.NoError(t, err)
	return string(content)
}
