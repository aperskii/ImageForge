package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"imageforge/internal/adapters/localstorage"
	"imageforge/internal/adapters/memqueue"
	"imageforge/internal/adapters/memrepo"
	"imageforge/internal/domain"
	"imageforge/internal/usecase"
)

const validSpec = `{"width":800,"height":600,"format":"webp","quality":82,"strip_metadata":true}`

// testStack is a fully wired API over in-memory adapters, plus the adapters
// themselves so tests can assert on the side effects.
type testStack struct {
	server  *httptest.Server
	storage *localstorage.Storage
	queue   *memqueue.Queue
	jobs    *memrepo.JobRepository
}

// newTestStack starts an API backed by a temporary directory and in-memory
// adapters, torn down when the test ends.
func newTestStack(t *testing.T, cfg Config) *testStack {
	t.Helper()

	storage, err := localstorage.New(t.TempDir())
	require.NoError(t, err)

	queue := memqueue.New(16)
	t.Cleanup(queue.Close)
	jobs := memrepo.New()

	if cfg.Logger == nil {
		// Discard the request log: these tests assert on responses, not output.
		cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	api := New(usecase.NewCreateJob(storage, jobs, queue), jobs, cfg)
	server := httptest.NewServer(api.Routes())
	t.Cleanup(server.Close)

	return &testStack{server: server, storage: storage, queue: queue, jobs: jobs}
}

// multipartBody builds an upload body. A file part is omitted when fileField is
// empty, and so is the spec part when specField is empty.
func multipartBody(
	t *testing.T,
	fileField, fileName, fileContent, specField, spec string,
) (body io.Reader, contentType string) {
	t.Helper()

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	if fileField != "" {
		part, err := writer.CreateFormFile(fileField, fileName)
		require.NoError(t, err)
		_, err = io.WriteString(part, fileContent)
		require.NoError(t, err)
	}
	if specField != "" {
		require.NoError(t, writer.WriteField(specField, spec))
	}
	require.NoError(t, writer.Close())

	return &buf, writer.FormDataContentType()
}

// decodeJob decodes a successful job response.
func decodeJob(t *testing.T, body io.Reader) jobResponse {
	t.Helper()

	var job jobResponse
	require.NoError(t, json.NewDecoder(body).Decode(&job))
	return job
}

// decodeError decodes an error response.
func decodeError(t *testing.T, body io.Reader) errorResponse {
	t.Helper()

	var resp errorResponse
	require.NoError(t, json.NewDecoder(body).Decode(&resp))
	return resp
}

func TestPostUploadsSuccess(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		spec       string
		publicBase string
		wantSpec   transformationSpec
	}{
		{
			name:     "full specification",
			spec:     validSpec,
			wantSpec: transformationSpec{Width: 800, Height: 600, Format: "webp", Quality: 82, StripMetadata: true},
		},
		{
			name:     "width only",
			spec:     `{"width":320,"format":"jpeg","quality":70}`,
			wantSpec: transformationSpec{Width: 320, Format: "jpeg", Quality: 70},
		},
		{
			name:     "height only, lossless format with no quality",
			spec:     `{"height":240,"format":"png"}`,
			wantSpec: transformationSpec{Height: 240, Format: "png"},
		},
		{
			name:     "watermark requested",
			spec:     `{"width":640,"format":"avif","quality":50,"watermark":true}`,
			wantSpec: transformationSpec{Width: 640, Format: "avif", Quality: 50, Watermark: true},
		},
		{
			name:       "a public base url yields a result url once done",
			spec:       validSpec,
			publicBase: "https://cdn.example.com/",
			wantSpec:   transformationSpec{Width: 800, Height: 600, Format: "webp", Quality: 82, StripMetadata: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stack := newTestStack(t, Config{PublicBaseURL: tt.publicBase})
			body, contentType := multipartBody(t, FileField, "photo.png", "image-bytes", SpecField, tt.spec)

			resp, err := http.Post(stack.server.URL+"/uploads", contentType, body)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()

			require.Equal(t, http.StatusAccepted, resp.StatusCode)
			assert.Equal(t, "application/json; charset=utf-8", resp.Header.Get("Content-Type"))

			job := decodeJob(t, resp.Body)
			assert.NotEmpty(t, job.ID)
			assert.Equal(t, domain.StatusPending.String(), job.Status)
			assert.Equal(t, tt.wantSpec, job.Transformation)
			assert.Empty(t, job.ResultKey, "a pending job has no result yet")
			assert.Empty(t, job.ResultURL)
			assert.Empty(t, job.Error)
			assert.False(t, job.CreatedAt.IsZero())
			assert.Equal(t, "/jobs/"+job.ID, resp.Header.Get("Location"))

			// The job was persisted, the original stored and the id queued.
			stored, err := stack.jobs.Get(context.Background(), job.ID)
			require.NoError(t, err)
			assert.Equal(t, domain.StatusPending, stored.Status)
			assert.Equal(t, usecase.OriginalKey(job.ID), stored.OriginalKey)

			object, err := stack.storage.Get(context.Background(), stored.OriginalKey)
			require.NoError(t, err)
			defer func() { _ = object.Close() }()
			content, err := io.ReadAll(object)
			require.NoError(t, err)
			assert.Equal(t, "image-bytes", string(content), "the uploaded bytes reach storage unchanged")

			assert.Equal(t, 1, stack.queue.Len(), "the job was enqueued exactly once")
		})
	}
}

func TestPostUploadsValidationErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		fileField  string
		specField  string
		spec       string
		wantStatus int
		wantCode   string
	}{
		{
			name:      "missing file part",
			fileField: "", specField: SpecField, spec: validSpec,
			wantStatus: http.StatusBadRequest, wantCode: codeMissingFile,
		},
		{
			name:      "file under the wrong field name",
			fileField: "upload", specField: SpecField, spec: validSpec,
			wantStatus: http.StatusBadRequest, wantCode: codeMissingFile,
		},
		{
			name:      "missing spec part",
			fileField: FileField, specField: "",
			wantStatus: http.StatusBadRequest, wantCode: codeInvalidSpec,
		},
		{
			name:      "empty spec part",
			fileField: FileField, specField: SpecField, spec: "",
			wantStatus: http.StatusBadRequest, wantCode: codeInvalidSpec,
		},
		{
			name:      "spec is not json",
			fileField: FileField, specField: SpecField, spec: "not json",
			wantStatus: http.StatusBadRequest, wantCode: codeInvalidSpec,
		},
		{
			name:      "spec has an unknown field",
			fileField: FileField, specField: SpecField, spec: `{"width":800,"format":"png","rotate":90}`,
			wantStatus: http.StatusBadRequest, wantCode: codeInvalidSpec,
		},
		{
			name:      "spec has a wrongly typed field",
			fileField: FileField, specField: SpecField, spec: `{"width":"800","format":"png"}`,
			wantStatus: http.StatusBadRequest, wantCode: codeInvalidSpec,
		},
		{
			name:      "no dimension requested",
			fileField: FileField, specField: SpecField, spec: `{"format":"png"}`,
			wantStatus: http.StatusBadRequest, wantCode: codeInvalidTransformation,
		},
		{
			name:      "negative dimension",
			fileField: FileField, specField: SpecField, spec: `{"width":-10,"format":"png"}`,
			wantStatus: http.StatusBadRequest, wantCode: codeInvalidTransformation,
		},
		{
			name:      "dimension above the maximum",
			fileField: FileField, specField: SpecField, spec: `{"width":10001,"format":"png"}`,
			wantStatus: http.StatusBadRequest, wantCode: codeInvalidTransformation,
		},
		{
			name:      "unsupported format",
			fileField: FileField, specField: SpecField, spec: `{"width":800,"format":"gif"}`,
			wantStatus: http.StatusBadRequest, wantCode: codeInvalidTransformation,
		},
		{
			name:      "missing format",
			fileField: FileField, specField: SpecField, spec: `{"width":800}`,
			wantStatus: http.StatusBadRequest, wantCode: codeInvalidTransformation,
		},
		{
			name:      "quality out of range",
			fileField: FileField, specField: SpecField, spec: `{"width":800,"format":"jpeg","quality":101}`,
			wantStatus: http.StatusBadRequest, wantCode: codeInvalidTransformation,
		},
		{
			name:      "quality set on a lossless format",
			fileField: FileField, specField: SpecField, spec: `{"width":800,"format":"png","quality":80}`,
			wantStatus: http.StatusBadRequest, wantCode: codeInvalidTransformation,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stack := newTestStack(t, Config{})
			body, contentType := multipartBody(t, tt.fileField, "photo.png", "image-bytes", tt.specField, tt.spec)

			resp, err := http.Post(stack.server.URL+"/uploads", contentType, body)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()

			require.Equal(t, tt.wantStatus, resp.StatusCode)

			errResp := decodeError(t, resp.Body)
			assert.Equal(t, tt.wantCode, errResp.Error.Code)
			assert.NotEmpty(t, errResp.Error.Message)
			assert.NotEmpty(t, errResp.Error.RequestID, "errors carry the request id")

			// A rejected upload leaves no trace behind.
			assert.Zero(t, stack.jobs.Len(), "no job is persisted")
			assert.Zero(t, stack.queue.Len(), "nothing is enqueued")
		})
	}
}

func TestPostUploadsRejectsNonMultipart(t *testing.T) {
	t.Parallel()

	stack := newTestStack(t, Config{})

	resp, err := http.Post(stack.server.URL+"/uploads", "application/json", strings.NewReader(validSpec))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Equal(t, codeInvalidMultipart, decodeError(t, resp.Body).Error.Code)
}

func TestPostUploadsRejectsOversizedBody(t *testing.T) {
	t.Parallel()

	const limit = 1 << 10
	stack := newTestStack(t, Config{MaxUploadBytes: limit})
	body, contentType := multipartBody(t, FileField, "big.png", strings.Repeat("x", 4*limit), SpecField, validSpec)

	resp, err := http.Post(stack.server.URL+"/uploads", contentType, body)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode)

	errResp := decodeError(t, resp.Body)
	assert.Equal(t, codePayloadTooLarge, errResp.Error.Code)
	assert.Contains(t, errResp.Error.Message, fmt.Sprint(limit), "the limit is stated in the message")
	assert.Zero(t, stack.jobs.Len())
}

func TestGetJob(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		publicBase string
		mutate     func(t *testing.T, stack *testStack, id string)
		wantStatus string
		assertJob  func(t *testing.T, job jobResponse)
	}{
		{
			name:       "a freshly created job is pending",
			wantStatus: domain.StatusPending.String(),
			assertJob: func(t *testing.T, job jobResponse) {
				t.Helper()
				assert.Empty(t, job.ResultKey)
				assert.Empty(t, job.ResultURL)
				assert.Empty(t, job.Error)
			},
		},
		{
			name: "a claimed job is processing",
			mutate: func(t *testing.T, stack *testStack, id string) {
				t.Helper()
				require.NoError(t, stack.jobs.UpdateStatus(
					context.Background(), id, domain.StatusProcessing, "", nil))
			},
			wantStatus: domain.StatusProcessing.String(),
			assertJob: func(t *testing.T, job jobResponse) {
				t.Helper()
				assert.Empty(t, job.ResultKey)
			},
		},
		{
			name:       "a finished job carries its result key and url",
			publicBase: "https://cdn.example.com",
			mutate: func(t *testing.T, stack *testStack, id string) {
				t.Helper()
				require.NoError(t, stack.jobs.UpdateStatus(
					context.Background(), id, domain.StatusDone, "results/"+id+".webp", nil))
			},
			wantStatus: domain.StatusDone.String(),
			assertJob: func(t *testing.T, job jobResponse) {
				t.Helper()
				assert.Equal(t, "results/"+job.ID+".webp", job.ResultKey)
				assert.Equal(t, "https://cdn.example.com/results/"+job.ID+".webp", job.ResultURL)
				assert.Empty(t, job.Error)
			},
		},
		{
			name: "a finished job with no configured base url carries the key alone",
			mutate: func(t *testing.T, stack *testStack, id string) {
				t.Helper()
				require.NoError(t, stack.jobs.UpdateStatus(
					context.Background(), id, domain.StatusDone, "results/"+id+".webp", nil))
			},
			wantStatus: domain.StatusDone.String(),
			assertJob: func(t *testing.T, job jobResponse) {
				t.Helper()
				assert.Equal(t, "results/"+job.ID+".webp", job.ResultKey)
				assert.Empty(t, job.ResultURL, "no base url means no result url")
			},
		},
		{
			name: "a failed job carries its reason",
			mutate: func(t *testing.T, stack *testStack, id string) {
				t.Helper()
				require.NoError(t, stack.jobs.UpdateStatus(
					context.Background(), id, domain.StatusFailed, "", assert.AnError))
			},
			wantStatus: domain.StatusFailed.String(),
			assertJob: func(t *testing.T, job jobResponse) {
				t.Helper()
				assert.Equal(t, assert.AnError.Error(), job.Error)
				assert.Empty(t, job.ResultKey)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stack := newTestStack(t, Config{PublicBaseURL: tt.publicBase})
			id := createJob(t, stack, validSpec)

			if tt.mutate != nil {
				tt.mutate(t, stack, id)
			}

			resp, err := http.Get(stack.server.URL + "/jobs/" + id)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()

			require.Equal(t, http.StatusOK, resp.StatusCode)

			job := decodeJob(t, resp.Body)
			assert.Equal(t, id, job.ID)
			assert.Equal(t, tt.wantStatus, job.Status)
			assert.Equal(t, transformationSpec{
				Width: 800, Height: 600, Format: "webp", Quality: 82, StripMetadata: true,
			}, job.Transformation)
			tt.assertJob(t, job)
		})
	}
}

func TestGetJobNotFound(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		id   string
	}{
		{name: "unknown id", id: "0123456789abcdef0123456789abcdef"},
		{name: "id that is not a hex string", id: "not-a-job"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stack := newTestStack(t, Config{})

			resp, err := http.Get(stack.server.URL + "/jobs/" + tt.id)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()

			require.Equal(t, http.StatusNotFound, resp.StatusCode)

			errResp := decodeError(t, resp.Body)
			assert.Equal(t, codeJobNotFound, errResp.Error.Code)
			assert.Contains(t, errResp.Error.Message, tt.id)
			assert.NotEmpty(t, errResp.Error.RequestID)
		})
	}
}

func TestUploadThenPollRoundTrip(t *testing.T) {
	t.Parallel()

	stack := newTestStack(t, Config{PublicBaseURL: "https://cdn.example.com"})

	id := createJob(t, stack, validSpec)

	// The worker would do this; here the repository stands in for it.
	require.NoError(t, stack.jobs.UpdateStatus(
		context.Background(), id, domain.StatusProcessing, "", nil))
	require.NoError(t, stack.jobs.UpdateStatus(
		context.Background(), id, domain.StatusDone, "results/"+id+".webp", nil))

	resp, err := http.Get(stack.server.URL + "/jobs/" + id)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	job := decodeJob(t, resp.Body)
	assert.Equal(t, domain.StatusDone.String(), job.Status)
	assert.Equal(t, "https://cdn.example.com/results/"+id+".webp", job.ResultURL)
	assert.True(t, job.UpdatedAt.After(job.CreatedAt) || job.UpdatedAt.Equal(job.CreatedAt))
}

func TestHealthAndReadiness(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		path       string
		readyCheck func(context.Context) error
		wantStatus int
		wantBody   string
	}{
		{name: "healthz is always ok", path: "/healthz", wantStatus: http.StatusOK, wantBody: "ok"},
		{name: "readyz with no check is ready", path: "/readyz", wantStatus: http.StatusOK, wantBody: "ready"},
		{
			name: "readyz with a passing check is ready", path: "/readyz",
			readyCheck: func(context.Context) error { return nil },
			wantStatus: http.StatusOK, wantBody: "ready",
		},
		{
			name: "readyz with a failing check is unavailable", path: "/readyz",
			readyCheck: func(context.Context) error { return assert.AnError },
			wantStatus: http.StatusServiceUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stack := newTestStack(t, Config{ReadyCheck: tt.readyCheck})

			resp, err := http.Get(stack.server.URL + tt.path)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()

			require.Equal(t, tt.wantStatus, resp.StatusCode)

			if tt.wantBody == "" {
				assert.Equal(t, codeNotReady, decodeError(t, resp.Body).Error.Code)
				return
			}

			var body map[string]string
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
			assert.Equal(t, tt.wantBody, body["status"])
		})
	}
}

func TestRoutingErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
		wantCode   string
	}{
		{
			name: "unknown path", method: http.MethodGet, path: "/nope",
			wantStatus: http.StatusNotFound, wantCode: codeNotFound,
		},
		{
			name: "wrong method on uploads", method: http.MethodGet, path: "/uploads",
			wantStatus: http.StatusMethodNotAllowed, wantCode: codeMethodNotAllowed,
		},
		{
			name: "wrong method on a job", method: http.MethodDelete, path: "/jobs/abc",
			wantStatus: http.StatusMethodNotAllowed, wantCode: codeMethodNotAllowed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stack := newTestStack(t, Config{})

			req, err := http.NewRequestWithContext(context.Background(), tt.method, stack.server.URL+tt.path, http.NoBody)
			require.NoError(t, err)

			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()

			require.Equal(t, tt.wantStatus, resp.StatusCode)
			assert.Equal(t, tt.wantCode, decodeError(t, resp.Body).Error.Code)
		})
	}
}

func TestMiddleware(t *testing.T) {
	t.Parallel()

	t.Run("every response carries a request id", func(t *testing.T) {
		t.Parallel()

		stack := newTestStack(t, Config{})

		resp, err := http.Get(stack.server.URL + "/healthz")
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		assert.NotEmpty(t, resp.Header.Get("X-Request-Id"))
	})

	t.Run("cors preflight is answered", func(t *testing.T) {
		t.Parallel()

		stack := newTestStack(t, Config{AllowedOrigins: []string{"https://app.example.com"}})

		req, err := http.NewRequestWithContext(context.Background(),
			http.MethodOptions, stack.server.URL+"/uploads", http.NoBody)
		require.NoError(t, err)
		req.Header.Set("Origin", "https://app.example.com")
		req.Header.Set("Access-Control-Request-Method", http.MethodPost)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		assert.Equal(t, "https://app.example.com", resp.Header.Get("Access-Control-Allow-Origin"))
		assert.Contains(t, resp.Header.Get("Access-Control-Allow-Methods"), http.MethodPost)
	})

	t.Run("a disallowed origin gets no allow header", func(t *testing.T) {
		t.Parallel()

		stack := newTestStack(t, Config{AllowedOrigins: []string{"https://app.example.com"}})

		req, err := http.NewRequestWithContext(context.Background(),
			http.MethodGet, stack.server.URL+"/healthz", http.NoBody)
		require.NoError(t, err)
		req.Header.Set("Origin", "https://evil.example.com")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		assert.Empty(t, resp.Header.Get("Access-Control-Allow-Origin"))
	})

	t.Run("a panic becomes a logged 500", func(t *testing.T) {
		t.Parallel()

		var logged bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug}))

		router := New(nil, nil, Config{Logger: logger}).Routes()
		mux, ok := router.(interface {
			Get(pattern string, h http.HandlerFunc)
			ServeHTTP(http.ResponseWriter, *http.Request)
		})
		require.True(t, ok, "the router must accept an extra route")
		mux.Get("/boom", func(http.ResponseWriter, *http.Request) {
			panic("deliberate test panic")
		})

		server := httptest.NewServer(mux)
		t.Cleanup(server.Close)

		resp, err := http.Get(server.URL + "/boom")
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
		assert.Equal(t, codeInternal, decodeError(t, resp.Body).Error.Code)
		assert.Contains(t, logged.String(), "panic recovered")
		assert.Contains(t, logged.String(), "deliberate test panic")
	})
}

func TestNewAppliesDefaults(t *testing.T) {
	t.Parallel()

	api := New(nil, nil, Config{})

	assert.NotNil(t, api.cfg.Logger)
	assert.Equal(t, DefaultMaxUploadBytes, api.cfg.MaxUploadBytes)
	assert.Equal(t, []string{"*"}, api.cfg.AllowedOrigins)

	trimmed := New(nil, nil, Config{PublicBaseURL: "https://cdn.example.com/"})
	assert.Equal(t, "https://cdn.example.com", trimmed.cfg.PublicBaseURL,
		"a trailing slash is trimmed so result urls have exactly one")
}

// createJob uploads a file and returns the resulting job id.
func createJob(t *testing.T, stack *testStack, spec string) string {
	t.Helper()

	body, contentType := multipartBody(t, FileField, "photo.png", "image-bytes", SpecField, spec)

	resp, err := http.Post(stack.server.URL+"/uploads", contentType, body)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusAccepted, resp.StatusCode)

	return decodeJob(t, resp.Body).ID
}

// TestUploadTimestampsAreUTC guards the contract that timestamps are
// serialized in UTC, which clients rely on when comparing them.
func TestUploadTimestampsAreUTC(t *testing.T) {
	t.Parallel()

	stack := newTestStack(t, Config{})
	id := createJob(t, stack, validSpec)

	stored, err := stack.jobs.Get(context.Background(), id)
	require.NoError(t, err)

	assert.Equal(t, time.UTC, stored.CreatedAt.Location())
	assert.Equal(t, time.UTC, stored.UpdatedAt.Location())
}
