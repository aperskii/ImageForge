package domain

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTransformationSpecValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		spec    TransformationSpec
		wantErr []error
	}{
		{
			name: "width and height set",
			spec: TransformationSpec{Width: 800, Height: 600, Format: FormatJPEG, Quality: 85},
		},
		{
			name: "width only, height derived from aspect ratio",
			spec: TransformationSpec{Width: 800, Format: FormatWebP, Quality: 80},
		},
		{
			name: "height only, width derived from aspect ratio",
			spec: TransformationSpec{Height: 600, Format: FormatAVIF, Quality: 50},
		},
		{
			name: "lossless format with default quality",
			spec: TransformationSpec{Width: 128, Format: FormatPNG},
		},
		{
			name: "dimensions at the maximum",
			spec: TransformationSpec{Width: MaxDimension, Height: MaxDimension, Format: FormatJPEG, Quality: 100},
		},
		{
			name: "all flags enabled",
			spec: TransformationSpec{Width: 640, Format: FormatJPEG, Quality: 1, Watermark: true, StripMetadata: true},
		},
		{
			name:    "no dimension set",
			spec:    TransformationSpec{Format: FormatJPEG, Quality: 85},
			wantErr: []error{ErrInvalidDimensions},
		},
		{
			name:    "negative width",
			spec:    TransformationSpec{Width: -1, Height: 600, Format: FormatJPEG, Quality: 85},
			wantErr: []error{ErrInvalidDimensions},
		},
		{
			name:    "negative height",
			spec:    TransformationSpec{Width: 800, Height: -600, Format: FormatJPEG, Quality: 85},
			wantErr: []error{ErrInvalidDimensions},
		},
		{
			name:    "width above the maximum",
			spec:    TransformationSpec{Width: MaxDimension + 1, Format: FormatJPEG, Quality: 85},
			wantErr: []error{ErrInvalidDimensions},
		},
		{
			name:    "height above the maximum",
			spec:    TransformationSpec{Height: MaxDimension + 1, Format: FormatJPEG, Quality: 85},
			wantErr: []error{ErrInvalidDimensions},
		},
		{
			name:    "empty format",
			spec:    TransformationSpec{Width: 800, Quality: 85},
			wantErr: []error{ErrInvalidFormat},
		},
		{
			name:    "unsupported format",
			spec:    TransformationSpec{Width: 800, Format: Format("gif"), Quality: 85},
			wantErr: []error{ErrInvalidFormat},
		},
		{
			name:    "uppercase format is not normalized",
			spec:    TransformationSpec{Width: 800, Format: Format("JPEG"), Quality: 85},
			wantErr: []error{ErrInvalidFormat},
		},
		{
			name:    "negative quality",
			spec:    TransformationSpec{Width: 800, Format: FormatJPEG, Quality: -1},
			wantErr: []error{ErrInvalidQuality},
		},
		{
			name:    "quality above 100",
			spec:    TransformationSpec{Width: 800, Format: FormatJPEG, Quality: 101},
			wantErr: []error{ErrInvalidQuality},
		},
		{
			name:    "quality set on a lossless format",
			spec:    TransformationSpec{Width: 800, Format: FormatPNG, Quality: 80},
			wantErr: []error{ErrInvalidQuality},
		},
		{
			name:    "every field invalid at once",
			spec:    TransformationSpec{Width: -1, Format: Format("bmp"), Quality: 900},
			wantErr: []error{ErrInvalidDimensions, ErrInvalidFormat, ErrInvalidQuality},
		},
		{
			name:    "zero value",
			spec:    TransformationSpec{},
			wantErr: []error{ErrInvalidDimensions, ErrInvalidFormat},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.spec.Validate()

			if len(tt.wantErr) == 0 {
				require.NoError(t, err)
				return
			}

			require.Error(t, err)
			for _, want := range tt.wantErr {
				assert.ErrorIsf(t, err, want, "expected error to wrap %v", want)
			}
			for _, notWant := range []error{ErrInvalidDimensions, ErrInvalidFormat, ErrInvalidQuality} {
				if !containsErr(tt.wantErr, notWant) {
					assert.NotErrorIsf(t, err, notWant, "did not expect error to wrap %v", notWant)
				}
			}
		})
	}
}

func containsErr(errs []error, target error) bool {
	for _, err := range errs {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

func TestFormat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		format    Format
		wantValid bool
		wantExt   string
		wantLossy bool
	}{
		{name: "jpeg", format: FormatJPEG, wantValid: true, wantExt: "jpg", wantLossy: true},
		{name: "png", format: FormatPNG, wantValid: true, wantExt: "png", wantLossy: false},
		{name: "webp", format: FormatWebP, wantValid: true, wantExt: "webp", wantLossy: true},
		{name: "avif", format: FormatAVIF, wantValid: true, wantExt: "avif", wantLossy: true},
		{name: "unknown", format: Format("tiff"), wantValid: false, wantExt: "", wantLossy: false},
		{name: "empty", format: Format(""), wantValid: false, wantExt: "", wantLossy: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.wantValid, tt.format.Valid())
			assert.Equal(t, tt.wantExt, tt.format.Ext())
			assert.Equal(t, tt.wantLossy, tt.format.Lossy())
			assert.Equal(t, string(tt.format), tt.format.String())
		})
	}
}
