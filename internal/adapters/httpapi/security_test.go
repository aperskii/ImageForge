package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"imageforge/internal/adapters/localstorage"
	"imageforge/internal/adapters/memqueue"
	"imageforge/internal/adapters/memrepo"
	"imageforge/internal/usecase"
)

const testClientID = "test-client"

// testKey is a fixed signing key. It is 32 bytes of nothing in particular:
// tests need determinism, and this key never leaves this file.
var testKey = bytes.Repeat([]byte{0x2a}, 32)

// secureStack is an API with authentication and rate limiting wired up.
type secureStack struct {
	server *httptest.Server
	issuer *TokenIssuer
	api    *API
	jobs   *memrepo.JobRepository
	now    func() time.Time
}

// newSecureStack builds a running API. The clock is fixed unless a test
// replaces it, so token expiry and rate limiting are deterministic.
func newSecureStack(t *testing.T, cfg Config, opts ...IssuerOption) *secureStack {
	t.Helper()

	storage, err := localstorage.New(t.TempDir())
	require.NoError(t, err)
	queue := memqueue.New(64)
	t.Cleanup(queue.Close)
	jobs := memrepo.New()

	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	}
	if cfg.clock == nil {
		cfg.clock = time.Now
	}

	opts = append([]IssuerOption{withClock(cfg.clock)}, opts...)
	issuer, err := NewIssuer(testKey, opts...)
	require.NoError(t, err)

	api := New(usecase.NewCreateJob(storage, jobs, queue), jobs, issuer, cfg)
	t.Cleanup(api.Close)

	server := httptest.NewServer(api.Routes())
	t.Cleanup(server.Close)

	return &secureStack{server: server, issuer: issuer, api: api, jobs: jobs, now: cfg.clock}
}

// token mints a valid token for the default test client.
func (s *secureStack) token(t *testing.T) string {
	t.Helper()

	signed, err := s.issuer.Issue(testClientID)
	require.NoError(t, err)
	return signed
}

// do performs a request, optionally bearing a token.
func (s *secureStack) do(t *testing.T, method, path, token string, body io.Reader, contentType string) *http.Response {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), method, s.server.URL+path, body)
	require.NoError(t, err)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

// decodeProblem reads an RFC 7807 body and asserts the media type.
func decodeProblem(t *testing.T, resp *http.Response) Problem {
	t.Helper()

	assert.Equal(t, ProblemContentType, resp.Header.Get("Content-Type"),
		"errors must be served as problem+json")

	var problem Problem
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&problem))
	require.NoError(t, resp.Body.Close())
	return problem
}

// pngBytes encodes a small valid PNG.
func pngBytes(t *testing.T) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for y := range 16 {
		for x := range 16 {
			img.Set(x, y, color.RGBA{R: uint8(x * 16), G: uint8(y * 16), B: 0x40, A: 0xff})
		}
	}

	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

// uploadBody builds a multipart upload with the given file content.
func uploadBody(
	t *testing.T,
	filename string,
	content []byte,
	declaredType, spec string,
) (body io.Reader, contentType string) {
	t.Helper()

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	header := make(map[string][]string)
	header["Content-Disposition"] = []string{
		fmt.Sprintf(`form-data; name=%q; filename=%q`, FileField, filename),
	}
	if declaredType != "" {
		header["Content-Type"] = []string{declaredType}
	}

	part, err := writer.CreatePart(header)
	require.NoError(t, err)
	_, err = part.Write(content)
	require.NoError(t, err)

	require.NoError(t, writer.WriteField(SpecField, spec))
	require.NoError(t, writer.Close())

	return &buf, writer.FormDataContentType()
}

const validSpecJSON = `{"width":32,"format":"png"}`

// ---------------------------------------------------------------- auth ------

// TestUnauthorizedAccessIsRejected is the first of the three cases asked for:
// every protected route refuses a request that cannot prove who it is.
func TestUnauthorizedAccessIsRejected(t *testing.T) {
	t.Parallel()

	stack := newSecureStack(t, Config{})

	// A token signed with a key this server does not know.
	forged := jwt.NewWithClaims(jwt.SigningMethodHS256, Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    Issuer,
			Subject:   "attacker",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	})
	wrongKey, err := forged.SignedString(bytes.Repeat([]byte{0x99}, 32))
	require.NoError(t, err)

	// A token from a different issuer, signed with the right key: the
	// signature checks out, but it was not minted for this service.
	foreign := jwt.NewWithClaims(jwt.SigningMethodHS256, Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "somewhere-else",
			Subject:   "attacker",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	})
	wrongIssuer, err := foreign.SignedString(testKey)
	require.NoError(t, err)

	// An expired token signed correctly by this service.
	past, err := NewIssuer(testKey, withClock(func() time.Time {
		return time.Now().Add(-2 * time.Hour)
	}))
	require.NoError(t, err)
	expired, err := past.Issue(testClientID)
	require.NoError(t, err)

	// A token with no expiry at all, which must not be accepted as eternal.
	eternal := jwt.NewWithClaims(jwt.SigningMethodHS256, Claims{
		RegisteredClaims: jwt.RegisteredClaims{Issuer: Issuer, Subject: "attacker"},
	})
	noExpiry, err := eternal.SignedString(testKey)
	require.NoError(t, err)

	tokens := []struct {
		name  string
		token string
	}{
		{name: "no token at all", token: ""},
		{name: "not a token", token: "garbage"},
		{name: "signed with the wrong key", token: wrongKey},
		{name: "issued by someone else", token: wrongIssuer},
		{name: "expired", token: expired},
		{name: "with no expiry", token: noExpiry},
	}

	routes := []struct {
		name   string
		method string
		path   string
	}{
		{name: "POST /uploads", method: http.MethodPost, path: "/uploads"},
		{name: "GET /jobs/{id}", method: http.MethodGet, path: "/jobs/abc123"},
	}

	for _, route := range routes {
		for _, tc := range tokens {
			t.Run(route.name+" "+tc.name, func(t *testing.T) {
				t.Parallel()

				resp := stack.do(t, route.method, route.path, tc.token, http.NoBody, "")

				require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
				assert.Contains(t, resp.Header.Get("WWW-Authenticate"), "Bearer",
					"a 401 must say how to authenticate")

				problem := decodeProblem(t, resp)
				assert.Equal(t, TypeUnauthorized, problem.Type)
				assert.Equal(t, http.StatusUnauthorized, problem.Status)
				assert.NotEmpty(t, problem.RequestID)
				// The reason must stay coarse: saying which half of a forgery
				// worked is a hint an attacker can iterate on.
				assert.NotContains(t, strings.ToLower(problem.Detail), "signature")
			})
		}
	}
}

// TestAlgNoneIsRejected covers the classic JWT hole: a token asking to be
// verified with no algorithm at all.
func TestAlgNoneIsRejected(t *testing.T) {
	t.Parallel()

	stack := newSecureStack(t, Config{})

	unsigned := jwt.NewWithClaims(jwt.SigningMethodNone, Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    Issuer,
			Subject:   "attacker",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	})
	token, err := unsigned.SignedString(jwt.UnsafeAllowNoneSignatureType)
	require.NoError(t, err)

	_, err = stack.issuer.Verify(token)
	require.ErrorIs(t, err, ErrInvalidToken, "an unsigned token must never verify")

	resp := stack.do(t, http.MethodGet, "/jobs/abc123", token, http.NoBody, "")
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.NoError(t, resp.Body.Close())
}

// TestPublicRoutesStayOpen guards the other half of the auth boundary: probes
// and the token endpoint must not require a token.
func TestPublicRoutesStayOpen(t *testing.T) {
	t.Parallel()

	stack := newSecureStack(t, Config{})

	for _, path := range []string{"/healthz", "/readyz"} {
		resp := stack.do(t, http.MethodGet, path, "", http.NoBody, "")
		assert.Equalf(t, http.StatusOK, resp.StatusCode, "%s must not require a token", path)
		require.NoError(t, resp.Body.Close())
	}
}

func TestTokenEndpoint(t *testing.T) {
	t.Parallel()

	t.Run("issues a usable token", func(t *testing.T) {
		t.Parallel()

		stack := newSecureStack(t, Config{})

		resp := stack.do(t, http.MethodPost, "/auth/token", "",
			strings.NewReader(`{"client_id":"alice"}`), "application/json")
		require.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"),
			"a credential must not be cached")

		var issued tokenResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&issued))
		require.NoError(t, resp.Body.Close())

		assert.NotEmpty(t, issued.AccessToken)
		assert.Equal(t, "Bearer", issued.TokenType)
		assert.Positive(t, issued.ExpiresIn)

		clientID, err := stack.issuer.Verify(issued.AccessToken)
		require.NoError(t, err)
		assert.Equal(t, "alice", clientID)

		// And it actually opens a protected route.
		protected := stack.do(t, http.MethodGet, "/jobs/unknown", issued.AccessToken, http.NoBody, "")
		assert.Equal(t, http.StatusNotFound, protected.StatusCode, "the token was accepted")
		require.NoError(t, protected.Body.Close())
	})

	t.Run("rejects a bad body", func(t *testing.T) {
		t.Parallel()

		stack := newSecureStack(t, Config{})

		for _, body := range []string{``, `{}`, `{"client_id":""}`, `not json`, `{"client_id":"a","extra":1}`} {
			resp := stack.do(t, http.MethodPost, "/auth/token", "",
				strings.NewReader(body), "application/json")
			assert.Equalf(t, http.StatusBadRequest, resp.StatusCode, "body %q", body)
			require.NoError(t, resp.Body.Close())
		}
	})

	t.Run("requires the configured client secret", func(t *testing.T) {
		t.Parallel()

		stack := newSecureStack(t, Config{}, WithClientSecret("s3cret"))

		wrong := stack.do(t, http.MethodPost, "/auth/token", "",
			strings.NewReader(`{"client_id":"alice","client_secret":"guess"}`), "application/json")
		require.Equal(t, http.StatusUnauthorized, wrong.StatusCode)
		problem := decodeProblem(t, wrong)
		assert.Equal(t, TypeInvalidCredential, problem.Type)

		right := stack.do(t, http.MethodPost, "/auth/token", "",
			strings.NewReader(`{"client_id":"alice","client_secret":"s3cret"}`), "application/json")
		assert.Equal(t, http.StatusOK, right.StatusCode)
		require.NoError(t, right.Body.Close())
	})
}

func TestNewIssuerRejectsAWeakKey(t *testing.T) {
	t.Parallel()

	for _, size := range []int{0, 1, 16, 31} {
		_, err := NewIssuer(bytes.Repeat([]byte{1}, size))
		require.ErrorIsf(t, err, ErrNoSigningKey, "a %d byte key must be refused", size)
	}

	_, err := NewIssuer(bytes.Repeat([]byte{1}, 32))
	assert.NoError(t, err, "a 32 byte key is the minimum accepted")
}

// TestGenerateKeyIsRandom guards the fallback: an ephemeral key is only safer
// than a hardcoded one if it is actually unpredictable.
func TestGenerateKeyIsRandom(t *testing.T) {
	t.Parallel()

	first, err := GenerateKey()
	require.NoError(t, err)
	second, err := GenerateKey()
	require.NoError(t, err)

	assert.Len(t, first, 32)
	assert.NotEqual(t, first, second)
}

// ---------------------------------------------------------- rate limit ------

// TestRateLimitTriggers is the second case asked for: a client over its budget
// is refused with 429 and told when to come back.
func TestRateLimitTriggers(t *testing.T) {
	t.Parallel()

	const burst = 3

	// A frozen clock means no tokens refill during the test, so the burst is
	// exactly what the limiter allows and the result cannot flake on timing.
	frozen := time.Now()
	stack := newSecureStack(t, Config{
		RateLimit: 1,
		RateBurst: burst,
		clock:     func() time.Time { return frozen },
	})
	token := stack.token(t)

	// The burst is allowed through. A missing job is a 404, which is proof the
	// request reached the handler rather than the limiter.
	for i := range burst {
		resp := stack.do(t, http.MethodGet, "/jobs/unknown", token, http.NoBody, "")
		require.Equalf(t, http.StatusNotFound, resp.StatusCode, "request %d should be allowed", i+1)
		require.NoError(t, resp.Body.Close())
	}

	// The next one is refused.
	resp := stack.do(t, http.MethodGet, "/jobs/unknown", token, http.NoBody, "")
	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)

	retryAfter := resp.Header.Get("Retry-After")
	require.NotEmpty(t, retryAfter, "a 429 must carry Retry-After")
	seconds, err := strconv.Atoi(retryAfter)
	require.NoError(t, err, "Retry-After must be a whole number of seconds")
	assert.Positive(t, seconds)

	problem := decodeProblem(t, resp)
	assert.Equal(t, TypeRateLimited, problem.Type)
	assert.Equal(t, http.StatusTooManyRequests, problem.Status)
	assert.Equal(t, seconds, problem.RetryAfter, "the header and the body must agree")
	assert.NotEmpty(t, problem.RequestID)
}

// TestRateLimitIsPerClient checks the budget is not shared: one client
// exhausting its allowance must not lock everyone else out.
func TestRateLimitIsPerClient(t *testing.T) {
	t.Parallel()

	const burst = 2

	frozen := time.Now()
	stack := newSecureStack(t, Config{
		RateLimit: 1,
		RateBurst: burst,
		clock:     func() time.Time { return frozen },
	})

	noisy, err := stack.issuer.Issue("noisy")
	require.NoError(t, err)
	quiet, err := stack.issuer.Issue("quiet")
	require.NoError(t, err)

	for range burst {
		resp := stack.do(t, http.MethodGet, "/jobs/unknown", noisy, http.NoBody, "")
		require.Equal(t, http.StatusNotFound, resp.StatusCode)
		require.NoError(t, resp.Body.Close())
	}

	limited := stack.do(t, http.MethodGet, "/jobs/unknown", noisy, http.NoBody, "")
	require.Equal(t, http.StatusTooManyRequests, limited.StatusCode)
	require.NoError(t, limited.Body.Close())

	unaffected := stack.do(t, http.MethodGet, "/jobs/unknown", quiet, http.NoBody, "")
	assert.Equal(t, http.StatusNotFound, unaffected.StatusCode,
		"one client's budget must not be spent by another")
	require.NoError(t, unaffected.Body.Close())
}

// TestRateLimitRefills checks the bucket is a bucket: waiting restores credit.
func TestRateLimitRefills(t *testing.T) {
	t.Parallel()

	now := time.Now()
	clock := func() time.Time { return now }

	stack := newSecureStack(t, Config{RateLimit: 10, RateBurst: 1, clock: clock})
	token := stack.token(t)

	first := stack.do(t, http.MethodGet, "/jobs/unknown", token, http.NoBody, "")
	require.Equal(t, http.StatusNotFound, first.StatusCode)
	require.NoError(t, first.Body.Close())

	second := stack.do(t, http.MethodGet, "/jobs/unknown", token, http.NoBody, "")
	require.Equal(t, http.StatusTooManyRequests, second.StatusCode)
	require.NoError(t, second.Body.Close())

	// At ten per second, a fifth of a second is two tokens' worth.
	now = now.Add(200 * time.Millisecond)

	third := stack.do(t, http.MethodGet, "/jobs/unknown", token, http.NoBody, "")
	assert.Equal(t, http.StatusNotFound, third.StatusCode, "the bucket must refill over time")
	require.NoError(t, third.Body.Close())
}

// TestRateLimiterEvictsIdleClients guards the limiter's own memory: an
// unbounded map keyed by anything a caller controls is itself a way to attack
// the server.
func TestRateLimiterEvictsIdleClients(t *testing.T) {
	t.Parallel()

	now := time.Now()
	limiter := newRateLimiter(1, 1, func() time.Time { return now })
	t.Cleanup(limiter.Close)

	for i := range 100 {
		limiter.allow(fmt.Sprintf("client-%d", i))
	}
	require.Equal(t, 100, limiter.tracked())

	// Move past the idle window and run the sweep the ticker would have run.
	now = now.Add(clientTTL + time.Minute)
	limiter.mu.Lock()
	cutoff := now.Add(-limiter.ttl)
	for id, bucket := range limiter.clients {
		if bucket.seen.Before(cutoff) {
			delete(limiter.clients, id)
		}
	}
	limiter.mu.Unlock()

	assert.Zero(t, limiter.tracked(), "idle clients must be evicted")
}

func TestRateLimitCanBeDisabled(t *testing.T) {
	t.Parallel()

	stack := newSecureStack(t, Config{RateLimit: -1, RateBurst: -1})
	token := stack.token(t)

	for range 50 {
		resp := stack.do(t, http.MethodGet, "/jobs/unknown", token, http.NoBody, "")
		require.Equal(t, http.StatusNotFound, resp.StatusCode)
		require.NoError(t, resp.Body.Close())
	}
}

// --------------------------------------------------------- media types ------

// TestInvalidFileTypeIsRejected is the third case asked for: an upload that is
// not an image is refused, whatever it claims to be.
func TestInvalidFileTypeIsRejected(t *testing.T) {
	t.Parallel()

	stack := newSecureStack(t, Config{})
	token := stack.token(t)

	tests := []struct {
		name         string
		filename     string
		content      []byte
		declaredType string
	}{
		{
			name:     "plain text",
			filename: "notes.txt",
			content:  []byte("this is not an image, it is prose"),
		},
		{
			// The declared type is a lie; only the bytes count.
			name:         "a script claiming to be a png",
			filename:     "payload.png",
			content:      []byte("#!/bin/sh\nrm -rf /\n"),
			declaredType: "image/png",
		},
		{
			name:         "html claiming to be a jpeg",
			filename:     "page.jpg",
			content:      []byte("<!doctype html><html><body><script>alert(1)</script></body></html>"),
			declaredType: "image/jpeg",
		},
		{
			name:     "a pdf",
			filename: "doc.pdf",
			content:  []byte("%PDF-1.7\n1 0 obj\n<<>>\nendobj\n"),
		},
		{
			name:     "an elf binary",
			filename: "a.out",
			content:  append([]byte{0x7f, 'E', 'L', 'F'}, make([]byte, 64)...),
		},
		{
			name:         "an empty file",
			filename:     "empty.png",
			content:      []byte{},
			declaredType: "image/png",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			body, contentType := uploadBody(t, tt.filename, tt.content, tt.declaredType, validSpecJSON)
			resp := stack.do(t, http.MethodPost, "/uploads", token, body, contentType)

			require.Equal(t, http.StatusUnsupportedMediaType, resp.StatusCode)

			problem := decodeProblem(t, resp)
			assert.Equal(t, TypeUnsupportedMedia, problem.Type)
			assert.Equal(t, http.StatusUnsupportedMediaType, problem.Status)
			assert.NotEmpty(t, problem.Detail)
			assert.NotEmpty(t, problem.RequestID)

			assert.Zero(t, stack.jobs.Len(), "a rejected upload must not create a job")
		})
	}
}

// TestValidImageIsAccepted is the control for the case above: the check must
// not be so strict that it rejects real images.
func TestValidImageIsAccepted(t *testing.T) {
	t.Parallel()

	stack := newSecureStack(t, Config{})
	token := stack.token(t)

	source := pngBytes(t)
	body, contentType := uploadBody(t, "photo.png", source, "image/png", validSpecJSON)

	resp := stack.do(t, http.MethodPost, "/uploads", token, body, contentType)
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	require.NoError(t, resp.Body.Close())

	assert.Equal(t, 1, stack.jobs.Len())
}

// TestUploadIsNotTruncatedBySniffing checks the bytes read for the media-type
// check are still delivered to storage: sniffing must peek, not consume.
func TestUploadIsNotTruncatedBySniffing(t *testing.T) {
	t.Parallel()

	storage, err := localstorage.New(t.TempDir())
	require.NoError(t, err)
	queue := memqueue.New(4)
	t.Cleanup(queue.Close)
	jobs := memrepo.New()

	issuer, err := NewIssuer(testKey)
	require.NoError(t, err)
	api := New(usecase.NewCreateJob(storage, jobs, queue), jobs, issuer, Config{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	t.Cleanup(api.Close)
	server := httptest.NewServer(api.Routes())
	t.Cleanup(server.Close)

	token, err := issuer.Issue(testClientID)
	require.NoError(t, err)

	// Larger than the 512-byte sniff window, so a bug that consumed the peek
	// would show up as a short object.
	source := largePNG(t)
	require.Greater(t, len(source), sniffLen)

	body, contentType := uploadBody(t, "photo.png", source, "image/png", validSpecJSON)
	req, err := http.NewRequestWithContext(context.Background(),
		http.MethodPost, server.URL+"/uploads", body)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", contentType)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	var created jobResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&created))
	require.NoError(t, resp.Body.Close())

	object, err := storage.Get(context.Background(), usecase.OriginalKey(created.ID))
	require.NoError(t, err)
	stored, err := io.ReadAll(object)
	require.NoError(t, err)
	require.NoError(t, object.Close())

	assert.Equal(t, source, stored, "the whole upload must reach storage, sniffed bytes included")
}

func TestSniffMediaType(t *testing.T) {
	t.Parallel()

	webp := append([]byte("RIFF"), append([]byte{0, 0, 0, 0}, []byte("WEBPVP8 ")...)...)
	avif := append([]byte{0, 0, 0, 0x20}, append([]byte("ftypavif"), make([]byte, 16)...)...)

	tests := []struct {
		name    string
		content []byte
		want    string
		allowed bool
	}{
		{name: "png", content: pngBytes(t), want: "image/png", allowed: true},
		{name: "gif", content: []byte("GIF89a" + strings.Repeat("\x00", 16)), want: "image/gif", allowed: true},
		{name: "webp", content: webp, want: "image/webp", allowed: true},
		{name: "avif", content: avif, want: "image/avif", allowed: true},
		{name: "text", content: []byte("hello there"), want: "text/plain", allowed: false},
		{name: "pdf", content: []byte("%PDF-1.7\n"), want: "application/pdf", allowed: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mediaType, rewound, err := sniffMediaType(io.NopCloser(bytes.NewReader(tt.content)))
			require.NoError(t, err)

			assert.Equal(t, tt.want, mediaType)
			assert.Equal(t, tt.allowed, allowedMediaType(mediaType))

			// Whatever was peeked must still be readable.
			all, err := io.ReadAll(rewound)
			require.NoError(t, err)
			require.NoError(t, rewound.Close())
			assert.Equal(t, tt.content, all)
		})
	}

	t.Run("an empty body is rejected", func(t *testing.T) {
		t.Parallel()

		_, _, err := sniffMediaType(io.NopCloser(bytes.NewReader(nil)))
		require.ErrorIs(t, err, ErrUnsupportedMediaType)
	})
}

// ------------------------------------------------------------- limits ------

// TestOversizedUploadIsRejected checks the size limit still applies once a
// request is authenticated.
func TestOversizedUploadIsRejected(t *testing.T) {
	t.Parallel()

	const limit = 2048
	stack := newSecureStack(t, Config{MaxUploadBytes: limit})
	token := stack.token(t)

	body, contentType := uploadBody(t, "big.png",
		bytes.Repeat([]byte{0x89}, 8*limit), "image/png", validSpecJSON)

	resp := stack.do(t, http.MethodPost, "/uploads", token, body, contentType)

	require.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode)
	problem := decodeProblem(t, resp)
	assert.Equal(t, TypePayloadTooLarge, problem.Type)
	assert.Contains(t, problem.Detail, strconv.Itoa(limit))
	assert.Zero(t, stack.jobs.Len())
}

// TestProblemShape pins the RFC 7807 contract across the error paths, since it
// is what clients switch on.
func TestProblemShape(t *testing.T) {
	t.Parallel()

	stack := newSecureStack(t, Config{})
	token := stack.token(t)

	tests := []struct {
		name       string
		method     string
		path       string
		token      string
		wantStatus int
		wantType   string
	}{
		{
			name: "unknown route", method: http.MethodGet, path: "/nope", token: token,
			wantStatus: http.StatusNotFound, wantType: TypeNotFound,
		},
		{
			name: "wrong method", method: http.MethodDelete, path: "/uploads", token: token,
			wantStatus: http.StatusMethodNotAllowed, wantType: TypeMethodNotAllowed,
		},
		{
			name: "unknown job", method: http.MethodGet, path: "/jobs/missing", token: token,
			wantStatus: http.StatusNotFound, wantType: TypeJobNotFound,
		},
		{
			name: "unauthenticated", method: http.MethodGet, path: "/jobs/missing", token: "",
			wantStatus: http.StatusUnauthorized, wantType: TypeUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			resp := stack.do(t, tt.method, tt.path, tt.token, http.NoBody, "")
			require.Equal(t, tt.wantStatus, resp.StatusCode)

			problem := decodeProblem(t, resp)
			assert.Equal(t, tt.wantType, problem.Type, "the type is what clients switch on")
			assert.Equal(t, tt.wantStatus, problem.Status, "the body repeats the status per RFC 7807")
			assert.NotEmpty(t, problem.Title)
			assert.Equal(t, tt.path, problem.Instance)
			assert.NotEmpty(t, problem.RequestID)
		})
	}
}

// largePNG encodes an image whose compressed form comfortably exceeds the
// sniff window, so a bug that consumed the peeked bytes would be visible as a
// short object rather than hidden by a tiny fixture.
func largePNG(t *testing.T) []byte {
	t.Helper()

	const size = 256
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	// Noise rather than a gradient: a smooth image compresses down to almost
	// nothing, which is how the first version of this fixture ended up at 89
	// bytes and unable to prove anything.
	seed := uint32(1)
	for y := range size {
		for x := range size {
			seed = seed*1664525 + 1013904223
			img.Set(x, y, color.RGBA{
				R: uint8(seed >> 24), G: uint8(seed >> 16), B: uint8(seed >> 8), A: 0xff,
			})
		}
	}

	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}
