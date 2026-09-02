// Package imageproc implements the ports.ImageProcessor port.
//
// Two interchangeable backends are provided, selected at compile time:
//
//   - the default backend binds libvips through govips, and supports every
//     format in domain.Format;
//   - the "nogovips" build tag selects a pure-Go backend built on image/draw
//     and golang.org/x/image, for environments where libvips is not installed.
//     It reads JPEG, PNG and WebP but writes JPEG and PNG only.
//
// Both backends expose the same API and share the Process pipeline below, so
// the choice is invisible to callers. Backend names the one in use.
package imageproc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"imageforge/internal/domain"
	"imageforge/internal/ports"
)

// Compile-time assertion that the selected backend satisfies the port.
var _ ports.ImageProcessor = (*Processor)(nil)

// Errors returned by both backends. They are wrapped with contextual detail,
// so compare them with errors.Is.
var (
	// ErrUnsupportedFormat is returned for an output format the active backend
	// cannot encode.
	ErrUnsupportedFormat = errors.New("unsupported output format")
	// ErrEmptySource is returned when the source image has no pixels or could
	// not be decoded into a sized image.
	ErrEmptySource = errors.New("source image has no dimensions")
	// ErrNoTargetSize is returned when a resize requests neither a width nor a
	// height.
	ErrNoTargetSize = errors.New("resize requires a width or a height")
	// ErrInvalidMaxDimension is returned when a thumbnail requests a
	// non-positive bounding dimension.
	ErrInvalidMaxDimension = errors.New("thumbnail requires a positive maximum dimension")
)

// DefaultWatermarkText is the overlay applied when no other text is configured.
const DefaultWatermarkText = "ImageForge"

// config holds the settings shared by both backends.
type config struct {
	watermarkText string
	// fontName is the libvips/Pango font specification used for the watermark.
	// The pure-Go backend ignores it and always uses its built-in bitmap face.
	fontName string
}

// Option overrides a Processor setting.
type Option func(*config)

// WithWatermarkText sets the text overlaid when TransformationSpec.Watermark is
// requested. An empty string leaves the default in place.
func WithWatermarkText(text string) Option {
	return func(c *config) {
		if text != "" {
			c.watermarkText = text
		}
	}
}

// WithFont sets the font specification used for the watermark by the govips
// backend, for example "sans 14". It is ignored by the pure-Go backend.
func WithFont(name string) Option {
	return func(c *config) {
		if name != "" {
			c.fontName = name
		}
	}
}

// newConfig applies opts on top of the defaults.
func newConfig(opts ...Option) config {
	cfg := config{
		watermarkText: DefaultWatermarkText,
		fontName:      "sans 12",
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	return cfg
}

// Process applies spec to input and returns the encoded result.
//
// The pipeline is: strip metadata, resize, watermark, encode. Metadata is
// stripped first so later stages cannot reintroduce it, and the watermark is
// applied after the resize so that it keeps a legible size relative to the
// output rather than being scaled down with the image.
//
// A zero Width or Height in spec means "derive this dimension from the other,
// preserving the source aspect ratio"; when both are set the image is resized
// to exactly those dimensions.
//
// ctx is honored between stages. The underlying transformations are
// synchronous and cannot themselves be interrupted, so cancellation takes
// effect at the next stage boundary rather than immediately.
func (p *Processor) Process(ctx context.Context, input io.Reader, spec domain.TransformationSpec) (io.Reader, error) {
	if input == nil {
		return nil, fmt.Errorf("imageproc: %w", ErrEmptySource)
	}
	if err := spec.Validate(); err != nil {
		return nil, fmt.Errorf("imageproc: %w", err)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}

	img, err := p.Open(input)
	if err != nil {
		return nil, fmt.Errorf("imageproc: open source: %w", err)
	}
	defer img.Close()

	if spec.StripMetadata {
		if err = img.StripMetadata(); err != nil {
			return nil, fmt.Errorf("imageproc: strip metadata: %w", err)
		}
	}

	if err = img.Resize(spec.Width, spec.Height, spec.Width == 0 || spec.Height == 0); err != nil {
		return nil, fmt.Errorf("imageproc: resize: %w", err)
	}

	if spec.Watermark {
		if err = img.Watermark(p.cfg.watermarkText); err != nil {
			return nil, fmt.Errorf("imageproc: watermark: %w", err)
		}
	}

	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}

	out, err := img.ConvertFormat(spec.Format.String(), spec.Quality)
	if err != nil {
		return nil, fmt.Errorf("imageproc: encode: %w", err)
	}
	return bytes.NewReader(out), nil
}

// targetSize computes the output dimensions of a resize.
//
// A non-positive wantW or wantH is derived from the other, preserving the
// source aspect ratio. When both are given, keepAspect decides between the
// largest box with the source ratio that fits inside wantW x wantH, and the
// exact wantW x wantH. The result is never smaller than 1x1.
func targetSize(srcW, srcH, wantW, wantH int, keepAspect bool) (width, height int, err error) {
	if srcW <= 0 || srcH <= 0 {
		return 0, 0, fmt.Errorf("%w: source is %dx%d", ErrEmptySource, srcW, srcH)
	}

	switch {
	case wantW <= 0 && wantH <= 0:
		return 0, 0, ErrNoTargetSize
	case wantW <= 0:
		return atLeastOne(scaleDimension(srcW, srcH, wantH)), atLeastOne(wantH), nil
	case wantH <= 0:
		return atLeastOne(wantW), atLeastOne(scaleDimension(srcH, srcW, wantW)), nil
	case !keepAspect:
		return atLeastOne(wantW), atLeastOne(wantH), nil
	}

	// Both dimensions given and the aspect ratio must be preserved: fit the
	// source inside the requested box.
	if srcW*wantH <= srcH*wantW {
		return atLeastOne(scaleDimension(srcW, srcH, wantH)), atLeastOne(wantH), nil
	}
	return atLeastOne(wantW), atLeastOne(scaleDimension(srcH, srcW, wantW)), nil
}

// thumbnailSize computes the dimensions of a thumbnail whose longest side is
// maxDim. Images already within the bound are returned unchanged, so a
// thumbnail never upscales.
func thumbnailSize(srcW, srcH, maxDim int) (width, height int, err error) {
	if srcW <= 0 || srcH <= 0 {
		return 0, 0, fmt.Errorf("%w: source is %dx%d", ErrEmptySource, srcW, srcH)
	}
	if maxDim <= 0 {
		return 0, 0, fmt.Errorf("%w: got %d", ErrInvalidMaxDimension, maxDim)
	}

	if srcW <= maxDim && srcH <= maxDim {
		return srcW, srcH, nil
	}
	if srcW >= srcH {
		return maxDim, atLeastOne(scaleDimension(srcH, srcW, maxDim)), nil
	}
	return atLeastOne(scaleDimension(srcW, srcH, maxDim)), maxDim, nil
}

// scaleDimension returns value scaled by target/reference, rounded to nearest.
func scaleDimension(value, reference, target int) int {
	return (value*target + reference/2) / reference
}

// atLeastOne clamps a computed dimension to a valid pixel count.
func atLeastOne(v int) int {
	if v < 1 {
		return 1
	}
	return v
}

// watermarkBox returns the size and corner margins of the watermark label for
// an image of w x h pixels, so that the overlay stays proportionate at any
// resolution.
func watermarkBox(w, h int) (boxW, boxH, marginX, marginY int) {
	return atLeastOne(w / 2), atLeastOne(h / 12), atLeastOne(w / 40), atLeastOne(h / 40)
}

// normalizeFormat maps a caller-supplied format name onto domain.Format.
func normalizeFormat(format string) (domain.Format, error) {
	f := domain.Format(format)
	if !f.Valid() {
		return "", fmt.Errorf("%w: %q", ErrUnsupportedFormat, format)
	}
	return f, nil
}
