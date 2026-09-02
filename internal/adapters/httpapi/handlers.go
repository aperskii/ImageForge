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
		a.writeUseCaseError(w, r, err)
		return
	}

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
			writeError(w, r, http.StatusRequestEntityTooLarge, codePayloadTooLarge,
				fmt.Sprintf("the request body exceeds the %d byte limit", a.cfg.MaxUploadBytes))
			return zero, nil, false
		}
		writeError(w, r, http.StatusBadRequest, codeInvalidMultipart,
			"the request body is not a valid multipart form")
		return zero, nil, false
	}

	file, _, err := r.FormFile(FileField)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, codeMissingFile,
			fmt.Sprintf("the %q part is required and must carry the image", FileField))
		return zero, nil, false
	}

	spec, ok := a.readSpec(w, r)
	if !ok {
		_ = file.Close()
		return zero, nil, false
	}
	return spec, file, true
}

// readSpec decodes the "spec" part, responding itself on any failure.
func (a *API) readSpec(w http.ResponseWriter, r *http.Request) (domain.TransformationSpec, bool) {
	var zero domain.TransformationSpec

	raw := r.FormValue(SpecField)
	if raw == "" {
		writeError(w, r, http.StatusBadRequest, codeInvalidSpec,
			fmt.Sprintf("the %q part is required and must carry a JSON transformation", SpecField))
		return zero, false
	}
	if len(raw) > maxSpecBytes {
		writeError(w, r, http.StatusBadRequest, codeInvalidSpec,
			fmt.Sprintf("the %q part exceeds %d bytes", SpecField, maxSpecBytes))
		return zero, false
	}

	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()

	var dto transformationSpec
	if err := decoder.Decode(&dto); err != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidSpec,
			fmt.Sprintf("the %q part is not a valid transformation: %v", SpecField, err))
		return zero, false
	}

	return dto.toDomain(), true
}

// writeUseCaseError maps a CreateJob failure onto a status code. Anything that
// is not the caller's fault is reported as a 500 without leaking internals.
func (a *API) writeUseCaseError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidDimensions),
		errors.Is(err, domain.ErrInvalidFormat),
		errors.Is(err, domain.ErrInvalidQuality):
		writeError(w, r, http.StatusBadRequest, codeInvalidTransformation, err.Error())
	case errors.Is(err, usecase.ErrNoSource):
		writeError(w, r, http.StatusBadRequest, codeMissingFile, err.Error())
	case errors.Is(err, r.Context().Err()) && r.Context().Err() != nil:
		// The client hung up; nothing useful can be written back.
		writeError(w, r, http.StatusRequestTimeout, codeInternal, "the request was canceled")
	default:
		a.cfg.Logger.ErrorContext(r.Context(), "creating the job failed",
			slog.String("error", err.Error()))
		writeError(w, r, http.StatusInternalServerError, codeInternal,
			"the job could not be created")
	}
}

// handleGetJob returns the current state of a job.
func (a *API) handleGetJob(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, r, http.StatusBadRequest, codeJobNotFound, "a job id is required")
		return
	}

	job, err := a.jobs.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, ports.ErrJobNotFound) {
			writeError(w, r, http.StatusNotFound, codeJobNotFound,
				fmt.Sprintf("no job with id %q", id))
			return
		}
		a.cfg.Logger.ErrorContext(r.Context(), "loading the job failed",
			slog.String("job_id", id),
			slog.String("error", err.Error()))
		writeError(w, r, http.StatusInternalServerError, codeInternal, "the job could not be loaded")
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
			a.cfg.Logger.WarnContext(r.Context(), "readiness check failed",
				slog.String("error", err.Error()))
			writeError(w, r, http.StatusServiceUnavailable, codeNotReady, "the service is not ready")
			return
		}
	}
	writeJSON(w, r, http.StatusOK, map[string]string{"status": "ready"})
}
