// Package s3storage implements the ports.Storage port on Amazon S3.
//
// It works equally against LocalStack, which is what the local stack and the
// integration tests use; the difference is confined to the client built by
// internal/adapters/awscfg.
package s3storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"imageforge/internal/ports"
)

// Compile-time assertion that Storage satisfies the port.
var _ ports.Storage = (*Storage)(nil)

// ErrInvalidKey is returned for an empty or otherwise unusable object key.
var ErrInvalidKey = errors.New("invalid storage key")

// DefaultPartSize is the multipart chunk size used for uploads. Images are
// small, so this mostly decides how much is buffered for a streaming body.
const DefaultPartSize int64 = 8 << 20 // 8MB

// Storage stores objects in an S3 bucket.
//
// It is safe for concurrent use.
type Storage struct {
	client *s3.Client
	// manager.Uploader is deprecated in favor of feature/s3/transfermanager,
	// which is still a v0 module. A pre-1.0 dependency on the path every
	// upload takes is the worse of the two risks, so this stays until that
	// package reaches v1.
	uploader *manager.Uploader //nolint:staticcheck // SA1019: see above.
	bucket   string
}

// Option overrides a Storage setting.
type Option func(*Storage)

// WithPartSize sets the multipart chunk size. A non-positive size leaves the
// default in place.
func WithPartSize(size int64) Option {
	return func(s *Storage) {
		if size > 0 {
			s.uploader.PartSize = size
		}
	}
}

// New wires a Storage to a bucket.
func New(client *s3.Client, bucket string, opts ...Option) (*Storage, error) {
	if client == nil {
		return nil, errors.New("s3storage: nil client")
	}
	if strings.TrimSpace(bucket) == "" {
		return nil, fmt.Errorf("s3storage: %w: bucket is empty", ErrInvalidKey)
	}

	storage := &Storage{
		client: client,
		bucket: bucket,
		uploader: manager.NewUploader(client, func(u *manager.Uploader) { //nolint:staticcheck // SA1019: see the field comment.
			u.PartSize = DefaultPartSize
		}),
	}
	for _, opt := range opts {
		opt(storage)
	}
	return storage, nil
}

// Bucket returns the bucket objects are stored in.
func (s *Storage) Bucket() string { return s.bucket }

// Put writes data under key, replacing any existing object.
//
// The upload goes through the SDK's manager, which streams a reader of unknown
// length rather than buffering all of it to compute a length up front.
func (s *Storage) Put(ctx context.Context, key string, data io.Reader) error {
	if err := validateKey(key); err != nil {
		return err
	}
	if data == nil {
		return fmt.Errorf("s3storage: put %q: no data", key)
	}

	//nolint:staticcheck // SA1019: see the uploader field comment.
	if _, err := s.uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   data,
	}); err != nil {
		return fmt.Errorf("s3storage: put %q: %w", key, err)
	}
	return nil
}

// Get opens the object stored under key. The caller closes the reader.
//
// A missing object returns an error wrapping fs.ErrNotExist, matching what the
// filesystem-backed adapter reports, so callers need not know which is behind
// the port.
func (s *Storage) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := validateKey(key); err != nil {
		return nil, err
	}

	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("s3storage: get %q: %w", key, fs.ErrNotExist)
		}
		return nil, fmt.Errorf("s3storage: get %q: %w", key, err)
	}
	return out.Body, nil
}

// isNotFound reports whether err says the object or bucket does not exist.
//
// S3 reports a missing key as NoSuchKey on GetObject, but a plain 404 with no
// typed error in some compatible implementations, so both are checked.
func isNotFound(err error) bool {
	var noSuchKey *types.NoSuchKey
	if errors.As(err, &noSuchKey) {
		return true
	}
	var noSuchBucket *types.NoSuchBucket
	if errors.As(err, &noSuchBucket) {
		return true
	}

	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound", "NoSuchBucket":
			return true
		}
	}
	return false
}

// validateKey rejects keys S3 cannot address.
func validateKey(key string) error {
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("s3storage: %w: key is empty", ErrInvalidKey)
	}
	if strings.Contains(key, "\x00") {
		return fmt.Errorf("s3storage: %w: key contains a null byte", ErrInvalidKey)
	}
	return nil
}
