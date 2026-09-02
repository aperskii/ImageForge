package imageproc

import (
	"bytes"
	"context"
	"image"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	// Register the decoders image.DecodeConfig needs to inspect the output of
	// either backend.
	_ "image/jpeg"
	_ "image/png"

	"imageforge/internal/domain"
)

// The sample images in testdata. Their dimensions are part of the fixture and
// are asserted against in TestTestdataFixtures.
const (
	gradientPNG = "gradient_64x48.png" // 64x48 landscape RGB gradient
	checkerJPG  = "checker_48x64.jpg"  // 48x64 portrait checkerboard
	alphaPNG    = "alpha_32x32.png"    // 32x32 square with a transparent border
)

func TestMain(m *testing.M) {
	code := m.Run()
	// libvips cannot be restarted, so this must be the last thing the test
	// binary does. It is a no-op for the pure-Go backend.
	Shutdown()
	os.Exit(code)
}

// TestTestdataFixtures pins the dimensions and formats of the sample images, so
// that a failure in the transformation tests cannot be blamed on the fixtures.
func TestTestdataFixtures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		file       string
		wantW      int
		wantH      int
		wantFormat string
	}{
		{file: gradientPNG, wantW: 64, wantH: 48, wantFormat: "png"},
		{file: checkerJPG, wantW: 48, wantH: 64, wantFormat: "jpeg"},
		{file: alphaPNG, wantW: 32, wantH: 32, wantFormat: "png"},
	}

	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			t.Parallel()

			cfg, format, err := image.DecodeConfig(bytes.NewReader(readTestdata(t, tt.file)))

			require.NoError(t, err)
			assert.Equal(t, tt.wantFormat, format)
			assert.Equal(t, tt.wantW, cfg.Width)
			assert.Equal(t, tt.wantH, cfg.Height)
		})
	}
}

func TestImageResize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		file       string
		width      int
		height     int
		keepAspect bool
		wantW      int
		wantH      int
		wantErr    error
	}{
		{
			name:  "width only derives the height from the aspect ratio",
			file:  gradientPNG,
			width: 32,
			wantW: 32, wantH: 24,
		},
		{
			name:   "height only derives the width from the aspect ratio",
			file:   gradientPNG,
			height: 24,
			wantW:  32, wantH: 24,
		},
		{
			name:   "both dimensions without keepAspect stretches to exactly that size",
			file:   gradientPNG,
			width:  40,
			height: 40,
			wantW:  40, wantH: 40,
		},
		{
			name:       "both dimensions with keepAspect fits inside the box",
			file:       gradientPNG,
			width:      40,
			height:     40,
			keepAspect: true,
			wantW:      40, wantH: 30,
		},
		{
			name:       "portrait source fits inside the box on its height",
			file:       checkerJPG,
			width:      40,
			height:     40,
			keepAspect: true,
			wantW:      30, wantH: 40,
		},
		{
			name:  "upscaling is allowed",
			file:  alphaPNG,
			width: 96,
			wantW: 96, wantH: 96,
		},
		{
			name:  "resizing to the current size is a no-op",
			file:  gradientPNG,
			width: 64, height: 48,
			wantW: 64, wantH: 48,
		},
		{
			name:    "no target size is rejected",
			file:    gradientPNG,
			wantErr: ErrNoTargetSize,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			img := openTestdata(t, tt.file)

			err := img.Resize(tt.width, tt.height, tt.keepAspect)

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)

			assert.Equal(t, tt.wantW, img.Width(), "reported width")
			assert.Equal(t, tt.wantH, img.Height(), "reported height")
			assertEncoded(t, img, domain.FormatPNG, 0, "png", tt.wantW, tt.wantH)
		})
	}
}

func TestImageGenerateThumbnail(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		file    string
		maxDim  int
		wantW   int
		wantH   int
		wantErr error
	}{
		{
			name:   "landscape is bounded on its width",
			file:   gradientPNG,
			maxDim: 32,
			wantW:  32, wantH: 24,
		},
		{
			name:   "portrait is bounded on its height",
			file:   checkerJPG,
			maxDim: 32,
			wantW:  24, wantH: 32,
		},
		{
			name:   "square is bounded on both sides",
			file:   alphaPNG,
			maxDim: 16,
			wantW:  16, wantH: 16,
		},
		{
			name:   "an image already within the bound is left alone",
			file:   gradientPNG,
			maxDim: 128,
			wantW:  64, wantH: 48,
		},
		{
			name:   "the bound is the longest side, not both",
			file:   gradientPNG,
			maxDim: 48,
			wantW:  48, wantH: 36,
		},
		{
			name:    "a non-positive bound is rejected",
			file:    gradientPNG,
			maxDim:  0,
			wantErr: ErrInvalidMaxDimension,
		},
		{
			name:    "a negative bound is rejected",
			file:    gradientPNG,
			maxDim:  -10,
			wantErr: ErrInvalidMaxDimension,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			img := openTestdata(t, tt.file)

			err := img.GenerateThumbnail(tt.maxDim)

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)

			assert.Equal(t, tt.wantW, img.Width(), "reported width")
			assert.Equal(t, tt.wantH, img.Height(), "reported height")
			assertEncoded(t, img, domain.FormatPNG, 0, "png", tt.wantW, tt.wantH)
		})
	}
}

// TestImageConvertFormat covers the formats both backends can encode. The
// backend-specific tests cover WebP and AVIF.
func TestImageConvertFormat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		file       string
		format     string
		quality    int
		wantFormat string
		wantW      int
		wantH      int
		wantErr    error
	}{
		{
			name: "png to jpeg", file: gradientPNG, format: "jpeg", quality: 80,
			wantFormat: "jpeg", wantW: 64, wantH: 48,
		},
		{
			name: "jpeg to png", file: checkerJPG, format: "png",
			wantFormat: "png", wantW: 48, wantH: 64,
		},
		{
			name: "png to png", file: alphaPNG, format: "png",
			wantFormat: "png", wantW: 32, wantH: 32,
		},
		{
			name: "jpeg to jpeg at the default quality", file: checkerJPG, format: "jpeg",
			wantFormat: "jpeg", wantW: 48, wantH: 64,
		},
		{
			name: "transparency survives a png round trip", file: alphaPNG, format: "png", quality: 0,
			wantFormat: "png", wantW: 32, wantH: 32,
		},
		{
			name: "an unknown format is rejected", file: gradientPNG, format: "gif",
			wantErr: ErrUnsupportedFormat,
		},
		{
			name: "an empty format is rejected", file: gradientPNG, format: "",
			wantErr: ErrUnsupportedFormat,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			img := openTestdata(t, tt.file)

			encoded, err := img.ConvertFormat(tt.format, tt.quality)

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.Nil(t, encoded)
				return
			}
			require.NoError(t, err)
			require.NotEmpty(t, encoded)

			cfg, format, err := image.DecodeConfig(bytes.NewReader(encoded))
			require.NoError(t, err)
			assert.Equal(t, tt.wantFormat, format)
			assert.Equal(t, tt.wantW, cfg.Width)
			assert.Equal(t, tt.wantH, cfg.Height)
		})
	}
}

// TestProcessorProcess exercises the whole pipeline through the port.
func TestProcessorProcess(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		file       string
		spec       domain.TransformationSpec
		wantFormat string
		wantW      int
		wantH      int
		wantErr    error
	}{
		{
			name:       "width only, converted to jpeg",
			file:       gradientPNG,
			spec:       domain.TransformationSpec{Width: 32, Format: domain.FormatJPEG, Quality: 80},
			wantFormat: "jpeg", wantW: 32, wantH: 24,
		},
		{
			name:       "height only, converted to png",
			file:       checkerJPG,
			spec:       domain.TransformationSpec{Height: 32, Format: domain.FormatPNG},
			wantFormat: "png", wantW: 24, wantH: 32,
		},
		{
			name:       "both dimensions resize to exactly that size",
			file:       gradientPNG,
			spec:       domain.TransformationSpec{Width: 40, Height: 40, Format: domain.FormatPNG},
			wantFormat: "png", wantW: 40, wantH: 40,
		},
		{
			name:       "metadata stripping does not disturb the pixels",
			file:       alphaPNG,
			spec:       domain.TransformationSpec{Width: 16, Format: domain.FormatPNG, StripMetadata: true},
			wantFormat: "png", wantW: 16, wantH: 16,
		},
		{
			name:       "watermarking an image large enough to hold the text",
			file:       gradientPNG,
			spec:       domain.TransformationSpec{Width: 256, Format: domain.FormatJPEG, Quality: 70, Watermark: true},
			wantFormat: "jpeg", wantW: 256, wantH: 192,
		},
		{
			name: "every option at once",
			file: checkerJPG,
			spec: domain.TransformationSpec{
				Width: 200, Height: 200, Format: domain.FormatPNG,
				Watermark: true, StripMetadata: true,
			},
			wantFormat: "png", wantW: 200, wantH: 200,
		},
		{
			name:    "an invalid spec is rejected before decoding",
			file:    gradientPNG,
			spec:    domain.TransformationSpec{Format: domain.FormatPNG},
			wantErr: domain.ErrInvalidDimensions,
		},
		{
			name:    "an unsupported format is rejected before decoding",
			file:    gradientPNG,
			spec:    domain.TransformationSpec{Width: 32, Format: domain.Format("gif")},
			wantErr: domain.ErrInvalidFormat,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			processor := newProcessor(t)
			source := bytes.NewReader(readTestdata(t, tt.file))

			out, err := processor.Process(context.Background(), source, tt.spec)

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.Nil(t, out)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, out)

			encoded, err := io.ReadAll(out)
			require.NoError(t, err)
			require.NotEmpty(t, encoded)

			cfg, format, err := image.DecodeConfig(bytes.NewReader(encoded))
			require.NoError(t, err)
			assert.Equal(t, tt.wantFormat, format)
			assert.Equal(t, tt.wantW, cfg.Width)
			assert.Equal(t, tt.wantH, cfg.Height)
		})
	}
}

func TestProcessorProcessRejectsBadInput(t *testing.T) {
	t.Parallel()

	spec := domain.TransformationSpec{Width: 32, Format: domain.FormatPNG}

	t.Run("nil source", func(t *testing.T) {
		t.Parallel()

		_, err := newProcessor(t).Process(context.Background(), nil, spec)

		require.ErrorIs(t, err, ErrEmptySource)
	})

	t.Run("undecodable source", func(t *testing.T) {
		t.Parallel()

		_, err := newProcessor(t).Process(context.Background(), bytes.NewReader([]byte("not an image")), spec)

		require.Error(t, err)
	})

	t.Run("canceled context", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := newProcessor(t).Process(ctx, bytes.NewReader(readTestdata(t, gradientPNG)), spec)

		require.ErrorIs(t, err, context.Canceled)
	})
}

func TestWithWatermarkText(t *testing.T) {
	t.Parallel()

	assert.Equal(t, DefaultWatermarkText, newConfig().watermarkText)
	assert.Equal(t, "ACME", newConfig(WithWatermarkText("ACME")).watermarkText)
	assert.Equal(t, DefaultWatermarkText, newConfig(WithWatermarkText("")).watermarkText,
		"an empty text leaves the default in place")
	assert.Equal(t, "serif 20", newConfig(WithFont("serif 20")).fontName)
}

func TestTargetSize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		srcW       int
		srcH       int
		wantW      int
		wantH      int
		keepAspect bool
		expectW    int
		expectH    int
		wantErr    error
	}{
		{name: "width only", srcW: 64, srcH: 48, wantW: 32, expectW: 32, expectH: 24},
		{name: "height only", srcW: 64, srcH: 48, wantH: 24, expectW: 32, expectH: 24},
		{name: "both, stretched", srcW: 64, srcH: 48, wantW: 40, wantH: 40, expectW: 40, expectH: 40},
		{
			name: "both, fitted on width", srcW: 64, srcH: 48, wantW: 40, wantH: 40,
			keepAspect: true, expectW: 40, expectH: 30,
		},
		{
			name: "both, fitted on height", srcW: 48, srcH: 64, wantW: 40, wantH: 40,
			keepAspect: true, expectW: 30, expectH: 40,
		},
		{
			name: "square source fits either way", srcW: 32, srcH: 32, wantW: 16, wantH: 16,
			keepAspect: true, expectW: 16, expectH: 16,
		},
		{name: "rounds to nearest", srcW: 64, srcH: 48, wantH: 25, expectW: 33, expectH: 25},
		{name: "clamps a collapsed dimension to one pixel", srcW: 1000, srcH: 1, wantW: 10, expectW: 10, expectH: 1},
		{name: "no target", srcW: 64, srcH: 48, wantErr: ErrNoTargetSize},
		{name: "negative target counts as unset", srcW: 64, srcH: 48, wantW: -5, wantH: -5, wantErr: ErrNoTargetSize},
		{name: "empty source", srcW: 0, srcH: 0, wantW: 32, wantErr: ErrEmptySource},
		{name: "negative source", srcW: -64, srcH: 48, wantW: 32, wantErr: ErrEmptySource},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w, h, err := targetSize(tt.srcW, tt.srcH, tt.wantW, tt.wantH, tt.keepAspect)

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expectW, w, "width")
			assert.Equal(t, tt.expectH, h, "height")
		})
	}
}

func TestThumbnailSize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		srcW    int
		srcH    int
		maxDim  int
		expectW int
		expectH int
		wantErr error
	}{
		{name: "landscape", srcW: 64, srcH: 48, maxDim: 32, expectW: 32, expectH: 24},
		{name: "portrait", srcW: 48, srcH: 64, maxDim: 32, expectW: 24, expectH: 32},
		{name: "square", srcW: 32, srcH: 32, maxDim: 16, expectW: 16, expectH: 16},
		{name: "never upscales", srcW: 64, srcH: 48, maxDim: 200, expectW: 64, expectH: 48},
		{name: "exactly at the bound", srcW: 64, srcH: 48, maxDim: 64, expectW: 64, expectH: 48},
		{name: "extreme ratio clamps to one pixel", srcW: 1000, srcH: 3, maxDim: 100, expectW: 100, expectH: 1},
		{name: "non-positive bound", srcW: 64, srcH: 48, maxDim: 0, wantErr: ErrInvalidMaxDimension},
		{name: "empty source", srcW: 0, srcH: 0, maxDim: 32, wantErr: ErrEmptySource},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w, h, err := thumbnailSize(tt.srcW, tt.srcH, tt.maxDim)

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expectW, w, "width")
			assert.Equal(t, tt.expectH, h, "height")
		})
	}
}

// newProcessor returns a Processor for the compiled-in backend.
func newProcessor(t *testing.T) *Processor {
	t.Helper()

	p, err := New()
	require.NoError(t, err, "the %s backend must be usable", Backend)
	return p
}

// readTestdata returns the raw bytes of a sample image.
func readTestdata(t *testing.T, name string) []byte {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	return data
}

// openTestdata decodes a sample image, closing it when the test ends.
func openTestdata(t *testing.T, name string) *Image {
	t.Helper()

	img, err := newProcessor(t).Open(bytes.NewReader(readTestdata(t, name)))
	require.NoError(t, err)
	t.Cleanup(img.Close)
	return img
}

// assertEncoded encodes img and asserts the format and dimensions that
// image.DecodeConfig reports for the result.
func assertEncoded(t *testing.T, img *Image, format domain.Format, quality int, wantFormat string, wantW, wantH int) {
	t.Helper()

	encoded, err := img.ConvertFormat(format.String(), quality)
	require.NoError(t, err)
	require.NotEmpty(t, encoded)

	cfg, gotFormat, err := image.DecodeConfig(bytes.NewReader(encoded))
	require.NoError(t, err)
	assert.Equal(t, wantFormat, gotFormat, "encoded format")
	assert.Equal(t, wantW, cfg.Width, "encoded width")
	assert.Equal(t, wantH, cfg.Height, "encoded height")
}
