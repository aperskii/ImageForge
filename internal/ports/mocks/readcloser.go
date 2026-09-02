package mocks

import (
	"io"
	"strings"
)

// ReadCloser adapts a string to an io.ReadCloser and records whether it was
// closed, so tests can assert that consumers release the source.
type ReadCloser struct {
	io.Reader
	Closed  bool
	CloseFn func() error
}

// NewReadCloser returns a ReadCloser over the given payload.
func NewReadCloser(payload string) *ReadCloser {
	return &ReadCloser{Reader: strings.NewReader(payload)}
}

// Close marks the reader closed and returns the error from CloseFn, if set.
func (r *ReadCloser) Close() error {
	r.Closed = true
	if r.CloseFn != nil {
		return r.CloseFn()
	}
	return nil
}
