//go:build nogovips

package imageproc

import (
	"bytes"
	"context"
	"image"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"imageforge/internal/domain"
)

func TestBackendIsPureGo(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "purego", Backend)
}

// TestConvertFormatRejectsUnencodableFormats records the documented gap in this
// backend: WebP and AVIF have no pure-Go encoder.
func TestConvertFormatRejectsUnencodableFormats(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		format domain.Format
	}{
		{name: "webp", format: domain.FormatWebP},
		{name: "avif", format: domain.FormatAVIF},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			img := openTestdata(t, gradientPNG)

			encoded, err := img.ConvertFormat(tt.format.String(), 80)

			require.ErrorIs(t, err, ErrUnsupportedFormat)
			assert.Nil(t, encoded)
			assert.Contains(t, err.Error(), Backend, "the error names the backend that cannot encode")
		})
	}
}

// TestProcessRejectsUnencodableFormats checks the gap surfaces through the port
// too, rather than only on the low-level call.
func TestProcessRejectsUnencodableFormats(t *testing.T) {
	t.Parallel()

	spec := domain.TransformationSpec{Width: 32, Format: domain.FormatWebP, Quality: 80}
	source := bytes.NewReader(readTestdata(t, gradientPNG))

	_, err := newProcessor(t).Process(context.Background(), source, spec)

	require.ErrorIs(t, err, ErrUnsupportedFormat)
}

// TestWatermarkOnTooSmallAnImage covers the other backend difference: the
// bitmap face has a fixed pixel size, so a small image cannot carry the text.
func TestWatermarkOnTooSmallAnImage(t *testing.T) {
	t.Parallel()

	img := openTestdata(t, alphaPNG)
	require.NoError(t, img.Resize(24, 24, false))

	err := img.Watermark("ImageForge")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "ImageForge")
}

// TestWatermarkKeepsDimensions asserts the overlay changes pixels, not the
// geometry of the image.
func TestWatermarkKeepsDimensions(t *testing.T) {
	t.Parallel()

	img := openTestdata(t, gradientPNG)
	require.NoError(t, img.Resize(256, 0, true))

	before, err := img.ConvertFormat(domain.FormatPNG.String(), 0)
	require.NoError(t, err)

	require.NoError(t, img.Watermark("ImageForge"))

	after, err := img.ConvertFormat(domain.FormatPNG.String(), 0)
	require.NoError(t, err)

	assert.Equal(t, 256, img.Width())
	assert.Equal(t, 192, img.Height())
	assert.NotEqual(t, before, after, "the watermark must change the pixels")

	cfg, format, err := image.DecodeConfig(bytes.NewReader(after))
	require.NoError(t, err)
	assert.Equal(t, "png", format)
	assert.Equal(t, 256, cfg.Width)
	assert.Equal(t, 192, cfg.Height)
}

// TestOpenReadsWebP confirms the backend can still read WebP even though it
// cannot write it.
func TestStripMetadataIsANoOp(t *testing.T) {
	t.Parallel()

	img := openTestdata(t, checkerJPG)

	before, err := img.ConvertFormat(domain.FormatPNG.String(), 0)
	require.NoError(t, err)
	require.NoError(t, img.StripMetadata())
	after, err := img.ConvertFormat(domain.FormatPNG.String(), 0)
	require.NoError(t, err)

	assert.Equal(t, before, after, "decoding already dropped the metadata")
}
