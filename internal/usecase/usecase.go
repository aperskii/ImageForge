package usecase

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"imageforge/internal/domain"
)

// IDFunc generates a unique job identifier.
type IDFunc func() string

// Clock reports the current time.
type Clock func() time.Time

// OriginalKey returns the storage key holding the uploaded source image of the
// job with the given id.
func OriginalKey(id string) string {
	return "originals/" + id
}

// ResultKey returns the storage key holding the transformed image of the job
// with the given id, for the output format described by spec.
func ResultKey(id string, spec domain.TransformationSpec) string {
	ext := spec.Format.Ext()
	if ext == "" {
		return "results/" + id
	}
	return fmt.Sprintf("results/%s.%s", id, ext)
}

// newID returns a random 128-bit identifier in hexadecimal form.
//
// It panics if the system CSPRNG is unavailable, which is an unrecoverable
// condition rather than a per-request failure.
func newID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		panic("imageforge: system CSPRNG unavailable: " + err.Error())
	}
	return hex.EncodeToString(buf[:])
}
