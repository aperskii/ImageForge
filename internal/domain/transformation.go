package domain

import (
	"errors"
	"fmt"
)

// Validation errors returned by TransformationSpec.Validate. They are wrapped
// with contextual detail, so compare them with errors.Is.
var (
	// ErrInvalidDimensions is returned for negative, oversized or entirely
	// unset width/height values.
	ErrInvalidDimensions = errors.New("invalid dimensions")
	// ErrInvalidFormat is returned for an unsupported output format.
	ErrInvalidFormat = errors.New("invalid format")
	// ErrInvalidQuality is returned for a quality outside the accepted range.
	ErrInvalidQuality = errors.New("invalid quality")
)

// MaxDimension is the largest width or height, in pixels, that a
// transformation may request.
const MaxDimension = 10000

// Format is a supported output image encoding.
type Format string

// The set of supported output formats.
const (
	FormatJPEG Format = "jpeg"
	FormatPNG  Format = "png"
	FormatWebP Format = "webp"
	FormatAVIF Format = "avif"
)

// String returns the wire representation of the format.
func (f Format) String() string { return string(f) }

// Valid reports whether f is a supported output format.
func (f Format) Valid() bool {
	switch f {
	case FormatJPEG, FormatPNG, FormatWebP, FormatAVIF:
		return true
	default:
		return false
	}
}

// Ext returns the canonical file extension for the format, without a leading
// dot. It returns an empty string for an unsupported format.
func (f Format) Ext() string {
	switch f {
	case FormatJPEG:
		return "jpg"
	case FormatPNG, FormatWebP, FormatAVIF:
		return string(f)
	default:
		return ""
	}
}

// Lossy reports whether the format encodes with a configurable quality.
func (f Format) Lossy() bool {
	return f == FormatJPEG || f == FormatWebP || f == FormatAVIF
}

// TransformationSpec describes the transformation to apply to a source image.
//
// A zero Width or Height means "derive this dimension from the other one,
// preserving the source aspect ratio"; at least one of them must be set. A zero
// Quality means "use the encoder default" and is the only accepted value for
// lossless formats.
type TransformationSpec struct {
	Width         int
	Height        int
	Format        Format
	Quality       int
	Watermark     bool
	StripMetadata bool
}

// Validate reports every problem with the specification, joined into a single
// error. It returns nil when the specification is usable.
func (s TransformationSpec) Validate() error {
	var errs []error

	switch {
	case s.Width < 0 || s.Height < 0:
		errs = append(errs, fmt.Errorf("%w: width %d and height %d must not be negative",
			ErrInvalidDimensions, s.Width, s.Height))
	case s.Width == 0 && s.Height == 0:
		errs = append(errs, fmt.Errorf("%w: at least one of width or height must be set",
			ErrInvalidDimensions))
	case s.Width > MaxDimension || s.Height > MaxDimension:
		errs = append(errs, fmt.Errorf("%w: width %d and height %d must not exceed %d",
			ErrInvalidDimensions, s.Width, s.Height, MaxDimension))
	}

	if !s.Format.Valid() {
		errs = append(errs, fmt.Errorf("%w: %q is not a supported output format",
			ErrInvalidFormat, s.Format))
	}

	switch {
	case s.Quality < 0 || s.Quality > 100:
		errs = append(errs, fmt.Errorf("%w: %d is outside the range 0-100",
			ErrInvalidQuality, s.Quality))
	case s.Quality != 0 && s.Format.Valid() && !s.Format.Lossy():
		errs = append(errs, fmt.Errorf("%w: format %s is lossless and accepts no quality",
			ErrInvalidQuality, s.Format))
	}

	return errors.Join(errs...)
}
