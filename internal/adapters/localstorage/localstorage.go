// Package localstorage implements the ports.Storage port on top of the local
// filesystem.
//
// It is intended for development and tests, where running the stack against S3
// or LocalStack would be unnecessary friction. Objects are stored as files
// under a root directory, with the object key used as a relative path.
package localstorage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"imageforge/internal/ports"
)

// Compile-time assertion that Storage satisfies the port.
var _ ports.Storage = (*Storage)(nil)

// ErrInvalidKey is returned for a key that does not name a location inside the
// storage root, such as one containing "..".
var ErrInvalidKey = errors.New("invalid storage key")

// Storage stores objects as files under a root directory.
//
// It is safe for concurrent use: writes go to a temporary file that is renamed
// into place, so a reader either sees the previous object or the new one, never
// a partial write.
type Storage struct {
	root string
}

// New returns a Storage rooted at dir, creating the directory if needed.
func New(dir string) (*Storage, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("localstorage: %w: root directory is empty", ErrInvalidKey)
	}

	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("localstorage: resolve root %q: %w", dir, err)
	}
	if err = os.MkdirAll(root, 0o750); err != nil {
		return nil, fmt.Errorf("localstorage: create root %q: %w", root, err)
	}
	return &Storage{root: root}, nil
}

// Root returns the absolute directory objects are stored under.
func (s *Storage) Root() string { return s.root }

// Put writes data under key, replacing any existing object.
//
// The data is written to a temporary file in the same directory and renamed
// into place, so a concurrent Get never observes a half-written object.
func (s *Storage) Put(ctx context.Context, key string, data io.Reader) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if data == nil {
		return fmt.Errorf("localstorage: put %q: no data", key)
	}

	path, err := s.resolve(key)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("localstorage: put %q: create directory: %w", key, err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return fmt.Errorf("localstorage: put %q: create temp file: %w", key, err)
	}
	tmpName := tmp.Name()
	// On any failure below the temporary file must not survive; removing it
	// after a successful rename is a no-op that reports a benign error.
	defer func() { _ = os.Remove(tmpName) }()

	if _, err = io.Copy(tmp, data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("localstorage: put %q: write: %w", key, err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("localstorage: put %q: close: %w", key, err)
	}
	if err = os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("localstorage: put %q: rename: %w", key, err)
	}
	return nil
}

// Get opens the object stored under key. The caller closes the reader.
//
// A missing object returns an error wrapping fs.ErrNotExist.
func (s *Storage) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	path, err := s.resolve(key)
	if err != nil {
		return nil, err
	}

	file, err := os.Open(path) //nolint:gosec // the path is confined to the root by resolve.
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("localstorage: get %q: %w", key, fs.ErrNotExist)
		}
		return nil, fmt.Errorf("localstorage: get %q: %w", key, err)
	}
	return file, nil
}

// resolve maps an object key onto an absolute path inside the storage root,
// rejecting anything that would escape it.
func (s *Storage) resolve(key string) (string, error) {
	if strings.TrimSpace(key) == "" {
		return "", fmt.Errorf("localstorage: %w: key is empty", ErrInvalidKey)
	}
	if strings.ContainsRune(key, '\x00') {
		return "", fmt.Errorf("localstorage: %w: key contains a null byte", ErrInvalidKey)
	}

	// Keys are always slash-separated, whatever the host filesystem uses.
	clean := filepath.Clean(filepath.FromSlash(key))
	if filepath.IsAbs(clean) || clean == "." || strings.HasPrefix(clean, "..") {
		return "", fmt.Errorf("localstorage: %w: %q escapes the storage root", ErrInvalidKey, key)
	}

	path := filepath.Join(s.root, clean)
	if path != s.root && !strings.HasPrefix(path, s.root+string(filepath.Separator)) {
		return "", fmt.Errorf("localstorage: %w: %q escapes the storage root", ErrInvalidKey, key)
	}
	return path, nil
}
