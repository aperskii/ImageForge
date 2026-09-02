package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"imageforge/internal/domain"
	"imageforge/internal/ports"
	"imageforge/internal/usecase"
)

// transformationSpec is the JSON shape of the "spec" upload field, and of the
// transformation echoed back on a job.
type transformationSpec struct {
	Width         int    `json:"width"`
	Height        int    `json:"height"`
	Format        string `json:"format"`
	Quality       int    `json:"quality"`
	Watermark     bool   `json:"watermark"`
	StripMetadata bool   `json:"strip_metadata"`
}

// toDomain maps the request shape onto the domain value object. Validation is
// the domain's job, not this function's.
func (s transformationSpec) toDomain() domain.TransformationSpec {
	return domain.TransformationSpec{
		Width:         s.Width,
		Height:        s.Height,
		Format:        domain.Format(s.Format),
		Quality:       s.Quality,
		Watermark:     s.Watermark,
		StripMetadata: s.StripMetadata,
	}
}

// specFromDomain maps a domain specification back onto the JSON shape.
func specFromDomain(spec domain.TransformationSpec) transformationSpec {
	return transformationSpec{
		Width:         spec.Width,
		Height:        spec.Height,
		Format:        spec.Format.String(),
		Quality:       spec.Quality,
		Watermark:     spec.Watermark,
		StripMetadata: spec.StripMetadata,
	}
}

// jobResponse is the JSON representation of a job.
type jobResponse struct {
	ID             string             `json:"id"`
	Status         string             `json:"status"`
	Transformation transformationSpec `json:"transformation"`
	CreatedAt      time.Time          `json:"created_at"`
	UpdatedAt      time.Time          `json:"updated_at"`
	// ResultKey is the storage key of the transformed image, set once the job
	// is done.
	ResultKey string `json:"result_key,omitempty"`
	// ResultURL is where the result can be fetched, set once the job is done
	// and only when the server is configured with a public base URL.
	ResultURL string `json:"result_url,omitempty"`
	// Error is the failure reason, set only when the job failed.
	Error string `json:"error,omitempty"`
}

// newJobResponse renders a job, deriving the result URL from the configured
// public base URL.
func (a *API) newJobResponse(job *domain.Job) jobResponse {
	resp := jobResponse{
		ID:             job.ID,
		Status:         job.Status.String(),
		Transformation: specFromDomain(job.Transformation),
		CreatedAt:      job.CreatedAt,
		UpdatedAt:      job.UpdatedAt,
		ResultKey:      job.ResultKey,
		Error:          job.Error,
	}
	if job.ResultKey != "" && a.cfg.PublicBaseURL != "" {
		resp.ResultURL = a.cfg.PublicBaseURL + "/" + job.ResultKey
	}
	return resp
}

// tokenRequest is the body of POST /auth/token.
type tokenRequest struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

// tokenResponse is an issued token, shaped like an OAuth 2 token response so
// existing clients need no special handling.
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

// handleToken issues a bearer token for a client.
//
// This is a demonstration credential flow, not an identity system: with no
// configured client secret it hands a token to whoever asks. What it does do
// properly is sign that token, pin the algorithm, and give it an expiry, so the
// middleware that consumes it is the real thing.
func (a *API) handleToken(w http.ResponseWriter, r *http.Request) {
	// The body is tiny; refuse to read a large one rather than buffer it.
	body := http.MaxBytesReader(w, r.Body, maxSpecBytes)

	var request tokenRequest
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeProblem(w, r, http.StatusBadRequest, TypeInvalidSpec, "Bad Request",
			`The body must be JSON with a "client_id", and a "client_secret" when the server requires one.`)
		return
	}

	clientID := strings.TrimSpace(request.ClientID)
	if clientID == "" {
		writeProblem(w, r, http.StatusBadRequest, TypeInvalidSpec, "Bad Request",
			`"client_id" is required.`)
		return
	}
	if len(clientID) > 128 {
		writeProblem(w, r, http.StatusBadRequest, TypeInvalidSpec, "Bad Request",
			`"client_id" must be 128 characters or fewer.`)
		return
	}

	if !a.issuer.Authorize(clientID, request.ClientSecret) {
		a.cfg.Logger.WarnContext(r.Context(), "rejected a token request",
			slogClient(clientID))
		writeProblem(w, r, http.StatusUnauthorized, TypeInvalidCredential, "Unauthorized",
			"The client id or secret is not recognized.")
		return
	}

	token, err := a.issuer.Issue(clientID)
	if err != nil {
		a.cfg.Logger.ErrorContext(r.Context(), "issuing a token failed", slogError(err))
		writeProblem(w, r, http.StatusInternalServerError, TypeInternal,
			"Internal Server Error", "The token could not be issued.")
		return
	}

	a.cfg.Logger.InfoContext(r.Context(), "issued a token", slogClient(clientID))

	// A credential must never be cached by a proxy or the browser.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, r, http.StatusOK, tokenResponse{
		AccessToken: token,
		TokenType:   "Bearer",
		ExpiresIn:   int(a.issuer.TTL().Seconds()),
	})
}

// handleUpload accepts a multipart upload and queues a transformation job.
//
// The request carries the image in the "file" part and the transformation in a
// "spec" part holding JSON. It returns 202 with the job, because the work has
// only been accepted at this point, not performed.
func (a *API) handleUpload(w http.ResponseWriter, r *http.Request) {
	spec, file, ok := a.readUpload(w, r)
	if !ok {
		return
	}
	defer func() { _ = file.Close() }()

	job, err := a.createJob.Execute(r.Context(), usecase.CreateJobInput{Source: file, Spec: spec})
	if err != nil {
		a.writeUseCaseProblem(w, r, err)
		return
	}

	clientID, _ := ClientID(r.Context())
	a.cfg.Logger.InfoContext(r.Context(), "accepted a job",
		slogClient(clientID), slog.String("job_id", job.ID))

	w.Header().Set("Location", "/jobs/"+job.ID)
	writeJSON(w, r, http.StatusAccepted, a.newJobResponse(job))
}

// readUpload parses and validates the multipart request, responding itself on
// any failure. The bool reports whether the caller should continue.
func (a *API) readUpload(w http.ResponseWriter, r *http.Request) (domain.TransformationSpec, io.ReadCloser, bool) {
	var zero domain.TransformationSpec

	// The RequestSize middleware already caps the body, but the handler must
	// not depend on being mounted behind it: an unbounded multipart parse is
	// how a single request exhausts the process.
	r.Body = http.MaxBytesReader(w, r.Body, a.cfg.MaxUploadBytes)

	// ParseMultipartForm buffers up to this much in memory and spills the rest
	// to disk; the total is bounded by the reader installed above.
	//nolint:gosec // G120: the body is bounded by the MaxBytesReader installed above.
	if err := r.ParseMultipartForm(multipartMemoryBytes); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeProblem(w, r, http.StatusRequestEntityTooLarge, TypePayloadTooLarge,
				"Payload Too Large",
				fmt.Sprintf("The request body exceeds the %d byte limit.", a.cfg.MaxUploadBytes))
			return zero, nil, false
		}
		writeProblem(w, r, http.StatusBadRequest, TypeInvalidMultipart, "Bad Request",
			"The request body is not a valid multipart form.")
		return zero, nil, false
	}

	file, header, err := r.FormFile(FileField)
	if err != nil {
		writeProblem(w, r, http.StatusBadRequest, TypeMissingFile, "Bad Request",
			fmt.Sprintf("The %q part is required and must carry the image.", FileField))
		return zero, nil, false
	}

	sniffed, ok := a.checkMediaType(w, r, file, header.Filename)
	if !ok {
		_ = file.Close()
		return zero, nil, false
	}

	spec, ok := a.readSpec(w, r)
	if !ok {
		_ = sniffed.Close()
		return zero, nil, false
	}
	return spec, sniffed, true
}

// checkMediaType rejects an upload whose bytes are not an image this service
// accepts, returning a reader that still yields the whole file.
//
// What the part declares as its Content-Type is chosen by the client and is
// worth nothing here; only the bytes are.
func (a *API) checkMediaType(
	w http.ResponseWriter,
	r *http.Request,
	file io.ReadCloser,
	filename string,
) (io.ReadCloser, bool) {
	mediaType, rewound, err := sniffMediaType(file)
	if err != nil {
		if errors.Is(err, ErrUnsupportedMediaType) {
			writeProblem(w, r, http.StatusUnsupportedMediaType, TypeUnsupportedMedia,
				"Unsupported Media Type", "The uploaded file is empty.")
			return nil, false
		}
		a.cfg.Logger.ErrorContext(r.Context(), "reading the upload failed", slogError(err))
		writeProblem(w, r, http.StatusBadRequest, TypeInvalidMultipart, "Bad Request",
			"The uploaded file could not be read.")
		return nil, false
	}

	if !allowedMediaType(mediaType) {
		clientID, _ := ClientID(r.Context())
		a.cfg.Logger.WarnContext(r.Context(), "rejected an upload on its media type",
			slogClient(clientID),
			slog.String("detected", mediaType),
			slog.String("declared", r.Header.Get("Content-Type")))

		_ = rewound.Close()
		writeProblem(w, r, http.StatusUnsupportedMediaType, TypeUnsupportedMedia,
			"Unsupported Media Type",
			fmt.Sprintf("%q is %s, which is not an accepted image type. Accepted types are %s.",
				filename, mediaType, strings.Join(AllowedMediaTypes, ", ")))
		return nil, false
	}

	return rewound, true
}

// readSpec decodes the "spec" part, responding itself on any failure.
func (a *API) readSpec(w http.ResponseWriter, r *http.Request) (domain.TransformationSpec, bool) {
	var zero domain.TransformationSpec

	raw := r.FormValue(SpecField)
	if raw == "" {
		writeProblem(w, r, http.StatusBadRequest, TypeInvalidSpec, "Bad Request",
			fmt.Sprintf("The %q part is required and must carry a JSON transformation.", SpecField))
		return zero, false
	}
	if len(raw) > maxSpecBytes {
		writeProblem(w, r, http.StatusBadRequest, TypeInvalidSpec, "Bad Request",
			fmt.Sprintf("The %q part exceeds %d bytes.", SpecField, maxSpecBytes))
		return zero, false
	}

	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()

	var dto transformationSpec
	if err := decoder.Decode(&dto); err != nil {
		writeProblem(w, r, http.StatusBadRequest, TypeInvalidSpec, "Bad Request",
			fmt.Sprintf("The %q part is not a valid transformation: %v", SpecField, err))
		return zero, false
	}

	return dto.toDomain(), true
}

// writeUseCaseProblem maps a CreateJob failure onto a status code. Anything
// that is not the caller's fault is reported as a 500 without leaking
// internals.
func (a *API) writeUseCaseProblem(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidDimensions),
		errors.Is(err, domain.ErrInvalidFormat),
		errors.Is(err, domain.ErrInvalidQuality):
		writeProblem(w, r, http.StatusBadRequest, TypeInvalidTransform,
			"Bad Request", err.Error())
	case errors.Is(err, usecase.ErrNoSource):
		writeProblem(w, r, http.StatusBadRequest, TypeMissingFile, "Bad Request", err.Error())
	case r.Context().Err() != nil:
		// The client hung up; nothing useful can be written back.
		writeProblem(w, r, http.StatusRequestTimeout, TypeInternal,
			"Request Timeout", "The request was canceled.")
	default:
		a.cfg.Logger.ErrorContext(r.Context(), "creating the job failed", slogError(err))
		writeProblem(w, r, http.StatusInternalServerError, TypeInternal,
			"Internal Server Error", "The job could not be created.")
	}
}

// handleGetJob returns the current state of a job.
func (a *API) handleGetJob(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeProblem(w, r, http.StatusBadRequest, TypeJobNotFound, "Bad Request",
			"A job id is required.")
		return
	}

	job, err := a.jobs.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, ports.ErrJobNotFound) {
			writeProblem(w, r, http.StatusNotFound, TypeJobNotFound, "Not Found",
				fmt.Sprintf("No job with id %q.", id))
			return
		}
		a.cfg.Logger.ErrorContext(r.Context(), "loading the job failed",
			slog.String("job_id", id), slogError(err))
		writeProblem(w, r, http.StatusInternalServerError, TypeInternal,
			"Internal Server Error", "The job could not be loaded.")
		return
	}

	writeJSON(w, r, http.StatusOK, a.newJobResponse(job))
}

// handleHealthz reports that the process is alive. It performs no dependency
// checks, so it stays cheap enough for a liveness probe.
func (a *API) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, http.StatusOK, map[string]string{"status": "ok"})
}

// handleReadyz reports whether the service can serve traffic, running the
// configured readiness check.
func (a *API) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if a.cfg.ReadyCheck != nil {
		if err := a.cfg.ReadyCheck(r.Context()); err != nil {
			a.cfg.Logger.WarnContext(r.Context(), "readiness check failed", slogError(err))
			writeProblem(w, r, http.StatusServiceUnavailable, TypeNotReady,
				"Service Unavailable", "The service is not ready.")
			return
		}
	}
	writeJSON(w, r, http.StatusOK, map[string]string{"status": "ready"})
}
