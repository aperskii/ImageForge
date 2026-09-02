package httpapi

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
)

// ErrUnsupportedMediaType is returned for an upload whose content is not an
// image this service accepts.
var ErrUnsupportedMediaType = errors.New("unsupported media type")

// AllowedMediaTypes are the source formats an upload may carry.
//
// GIF and BMP are decodable by at least one backend, so they are accepted as
// input even though neither is an output format.
var AllowedMediaTypes = []string{
	"image/jpeg",
	"image/png",
	"image/webp",
	"image/gif",
	"image/avif",
	"image/bmp",
}

// sniffLen is how many bytes http.DetectContentType inspects. Reading exactly
// this many keeps the peek bounded whatever the upload claims to be.
const sniffLen = 512

// sniffReader is an upload whose leading bytes have been inspected without
// being consumed.
type sniffReader struct {
	reader io.Reader
	closer io.Closer
}

func (s *sniffReader) Read(p []byte) (int, error) { return s.reader.Read(p) }

func (s *sniffReader) Close() error {
	if s.closer == nil {
		return nil
	}
	return s.closer.Close()
}

// sniffMediaType reads the first bytes of an upload to work out what it
// actually is, and returns a reader that still yields the whole thing.
//
// The declared Content-Type of a multipart part is chosen by the client and is
// worth nothing as a check: anything can claim to be image/png. What the bytes
// say is the only thing worth acting on.
func sniffMediaType(body io.ReadCloser) (mediaType string, rewound io.ReadCloser, err error) {
	buffered := bufio.NewReaderSize(body, sniffLen)

	head, err := buffered.Peek(sniffLen)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", nil, fmt.Errorf("read the start of the upload: %w", err)
	}
	if len(head) == 0 {
		return "", nil, fmt.Errorf("%w: the upload is empty", ErrUnsupportedMediaType)
	}

	detected := normalizeMediaType(http.DetectContentType(head))

	// http.DetectContentType predates both, so they are matched on their own
	// signatures. WebP is "RIFF????WEBP"; AVIF is an ISOBMFF box whose brand
	// says avif.
	switch {
	case len(head) >= 12 && string(head[0:4]) == "RIFF" && string(head[8:12]) == "WEBP":
		detected = "image/webp"
	case len(head) >= 12 && string(head[4:8]) == "ftyp" &&
		(string(head[8:12]) == "avif" || string(head[8:12]) == "avis"):
		detected = "image/avif"
	}

	return detected, &sniffReader{reader: buffered, closer: body}, nil
}

// normalizeMediaType strips any parameters and lowercases the type, so
// "image/jpeg; charset=utf-8" compares equal to "image/jpeg".
func normalizeMediaType(value string) string {
	base, _, _ := strings.Cut(value, ";")
	return strings.ToLower(strings.TrimSpace(base))
}

// allowedMediaType reports whether a detected type may be uploaded.
func allowedMediaType(mediaType string) bool {
	return slices.Contains(AllowedMediaTypes, mediaType)
}
