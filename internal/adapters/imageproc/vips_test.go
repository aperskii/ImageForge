//go:build !nogovips

package imageproc

import (
	"bytes"
	"context"
	"image"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	// x/image can decode WebP, which lets the WebP assertions below check real
	// dimensions rather than only a magic number.
	_ "golang.org/x/image/webp"

	"imageforge/internal/domain"
)

func TestBackendIsGovips(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "govips", Backend)
}

// TestConvertFormatWebP covers the format the pure-Go backend cannot encode.
func TestConvertFormatWebP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		file    string
		quality int
		wantW   int
		wantH   int
	}{
		{name: "from png", file: gradientPNG, quality: 80, wantW: 64, wantH: 48},
		{name: "from jpeg", file: checkerJPG, quality: 60, wantW: 48, wantH: 64},
		{name: "at the default quality", file: alphaPNG, wantW: 32, wantH: 32},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			img := openTestdata(t, tt.file)

			encoded, err := img.ConvertFormat(domain.FormatWebP.String(), tt.quality)

			require.NoError(t, err)
			require.NotEmpty(t, encoded)

			cfg, format, err := image.DecodeConfig(bytes.NewReader(encoded))
			require.NoError(t, err)
			assert.Equal(t, "webp", format)
			assert.Equal(t, tt.wantW, cfg.Width)
			assert.Equal(t, tt.wantH, cfg.Height)
		})
	}
}

// TestConvertFormatAVIF asserts the container rather than the pixels: Go has no
// AVIF decoder, so image.DecodeConfig cannot inspect the result.
func TestConvertFormatAVIF(t *testing.T) {
	t.Parallel()

	img := openTestdata(t, gradientPNG)

	encoded, err := img.ConvertFormat(domain.FormatAVIF.String(), 50)

	require.NoError(t, err)
	require.Greater(t, len(encoded), 12)
	assert.Equal(t, "ftyp", string(encoded[4:8]), "AVIF is an ISOBMFF container")
	assert.Equal(t, "avif", string(encoded[8:12]), "the major brand is avif")
}

// TestProcessToWebP exercises the full pipeline into a format only this backend
// supports.
func TestProcessToWebP(t *testing.T) {
	t.Parallel()

	spec := domain.TransformationSpec{Width: 32, Format: domain.FormatWebP, Quality: 75, StripMetadata: true}
	source := bytes.NewReader(readTestdata(t, gradientPNG))

	out, err := newProcessor(t).Process(context.Background(), source, spec)
	require.NoError(t, err)

	encoded, err := io.ReadAll(out)
	require.NoError(t, err)

	cfg, format, err := image.DecodeConfig(bytes.NewReader(encoded))
	require.NoError(t, err)
	assert.Equal(t, "webp", format)
	assert.Equal(t, 32, cfg.Width)
	assert.Equal(t, 24, cfg.Height)
}

// TestWatermarkScalesWithTheImage covers the govips-only behaviour: the label
// is sized relative to the image, so even a small one can carry the text.
func TestWatermarkScalesWithTheImage(t *testing.T) {
	t.Parallel()

	img := openTestdata(t, gradientPNG)

	before, err := img.ConvertFormat(domain.FormatPNG.String(), 0)
	require.NoError(t, err)

	require.NoError(t, img.Watermark("ImageForge"))

	after, err := img.ConvertFormat(domain.FormatPNG.String(), 0)
	require.NoError(t, err)

	assert.Equal(t, 64, img.Width(), "the watermark must not change the geometry")
	assert.Equal(t, 48, img.Height())
	assert.NotEqual(t, before, after, "the watermark must change the pixels")
}

// TestWatermarkEmptyTextIsANoOp documents that an unset watermark text leaves
// the image untouched rather than failing.
func TestWatermarkEmptyTextIsANoOp(t *testing.T) {
	t.Parallel()

	img := openTestdata(t, gradientPNG)

	before, err := img.ConvertFormat(domain.FormatPNG.String(), 0)
	require.NoError(t, err)

	require.NoError(t, img.Watermark(""))

	after, err := img.ConvertFormat(domain.FormatPNG.String(), 0)
	require.NoError(t, err)

	assert.Equal(t, before, after)
}

// TestStripMetadata checks the call succeeds and preserves the geometry; the
// absence of the metadata itself is libvips' contract, not ours to re-test.
func TestStripMetadata(t *testing.T) {
	t.Parallel()

	img := openTestdata(t, checkerJPG)

	require.NoError(t, img.StripMetadata())

	assert.Equal(t, 48, img.Width())
	assert.Equal(t, 64, img.Height())
}
