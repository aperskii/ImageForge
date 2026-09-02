package domain

import "time"

// JobStatus is the lifecycle state of a Job.
type JobStatus string

// The set of valid job statuses.
const (
	StatusPending    JobStatus = "pending"
	StatusProcessing JobStatus = "processing"
	StatusDone       JobStatus = "done"
	StatusFailed     JobStatus = "failed"
)

// String returns the wire representation of the status.
func (s JobStatus) String() string { return string(s) }

// Valid reports whether s is one of the known job statuses.
func (s JobStatus) Valid() bool {
	switch s {
	case StatusPending, StatusProcessing, StatusDone, StatusFailed:
		return true
	default:
		return false
	}
}

// Terminal reports whether the status is final, i.e. no further transition is
// expected for the job.
func (s JobStatus) Terminal() bool {
	return s == StatusDone || s == StatusFailed
}

// Job is a single image transformation request and its current state.
type Job struct {
	// ID uniquely identifies the job.
	ID string
	// OriginalKey is the storage key of the uploaded source image.
	OriginalKey string
	// ResultKey is the storage key of the transformed image. It is empty until
	// the job reaches StatusDone.
	ResultKey string
	// Status is the current lifecycle state of the job.
	Status JobStatus
	// Transformation is the requested transformation specification.
	Transformation TransformationSpec
	// CreatedAt is the instant the job was accepted.
	CreatedAt time.Time
	// UpdatedAt is the instant the job last changed state.
	UpdatedAt time.Time
	// Error holds the failure reason when Status is StatusFailed.
	Error string
}
