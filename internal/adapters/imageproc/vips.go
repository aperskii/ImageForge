//go:build !nogovips

package imageproc

import (
	"fmt"
	"io"
	"sync"

	"github.com/davidbyttow/govips/v2/vips"

	"imageforge/internal/domain"
)

// Backend names the compiled-in implementation.
const Backend = "govips"

// watermarkOpacity is the alpha applied to the watermark text, as a fraction.
const watermarkOpacity = 0.65

var (
	startupOnce sync.Once
	startupErr  error

	shutdownOnce sync.Once
)

// Processor transforms images with libvips, through govips.
//
// A Processor is safe for concurrent use: libvips is initialized once per
// process and each operation works on its own image handle.
type Processor struct {
	cfg config
}

// New initializes libvips if it is not already running and returns a Processor.
//
// libvips is a process-wide resource, so the first call performs the startup
// and later calls reuse it. Call Shutdown once, at process exit.
func New(opts ...Option) (*Processor, error) {
	startupOnce.Do(func() {
		vips.LoggingSettings(nil, vips.LogLevelError)
		startupErr = vips.Startup(nil)
	})
	if startupErr != nil {
		return nil, fmt.Errorf("imageproc: start libvips: %w", startupErr)
	}
	return &Processor{cfg: newConfig(opts...)}, nil
}

// Shutdown releases the process-wide libvips resources. It is safe to call more
// than once, but libvips cannot be restarted afterwards, so call it only when
// the process is exiting.
func Shutdown() {
	shutdownOnce.Do(vips.Shutdown)
}

// Image is a decoded image being transformed. Close it when done, to release
// the memory libvips holds for it.
type Image struct {
	cfg config
	ref *vips.ImageRef
}

// Open decodes the image read from r.
func (p *Processor) Open(r io.Reader) (*Image, error) {
	ref, err := vips.NewImageFromReader(r)
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return &Image{cfg: p.cfg, ref: ref}, nil
}

// Close releases the underlying libvips image. It is safe to call more than
// once.
func (im *Image) Close() {
	if im.ref != nil {
		im.ref.Close()
		im.ref = nil
	}
}

// Width returns the current width in pixels.
func (im *Image) Width() int { return im.ref.Width() }

// Height returns the current height in pixels.
func (im *Image) Height() int { return im.ref.Height() }

// Resize scales the image to width x height.
//
// A non-positive width or height is derived from the other, preserving the
// source aspect ratio. When both are given, keepAspect decides between fitting
// the image inside that box and stretching it to exactly those dimensions.
func (im *Image) Resize(width, height int, keepAspect bool) error {
	srcW, srcH := im.ref.Width(), im.ref.Height()

	targetW, targetH, err := targetSize(srcW, srcH, width, height, keepAspect)
	if err != nil {
		return err
	}
	if targetW == srcW && targetH == srcH {
		return nil
	}

	hScale := float64(targetW) / float64(srcW)
	vScale := float64(targetH) / float64(srcH)

	if hScale == vScale {
		if err = im.ref.Resize(hScale, vips.KernelLanczos3); err != nil {
			return fmt.Errorf("resize to %dx%d: %w", targetW, targetH, err)
		}
		return nil
	}

	if err = im.ref.ResizeWithVScale(hScale, vScale, vips.KernelLanczos3); err != nil {
		return fmt.Errorf("resize to %dx%d: %w", targetW, targetH, err)
	}
	return nil
}

// GenerateThumbnail scales the image down so its longest side is maxDim,
// preserving the aspect ratio. Images already within the bound are left
// untouched: a thumbnail never upscales.
func (im *Image) GenerateThumbnail(maxDim int) error {
	targetW, targetH, err := thumbnailSize(im.ref.Width(), im.ref.Height(), maxDim)
	if err != nil {
		return err
	}
	if targetW == im.ref.Width() && targetH == im.ref.Height() {
		return nil
	}

	if err = im.ref.Thumbnail(targetW, targetH, vips.InterestingNone); err != nil {
		return fmt.Errorf("thumbnail to %dx%d: %w", targetW, targetH, err)
	}
	return nil
}

// StripMetadata removes EXIF, XMP, IPTC and the embedded ICC profile.
func (im *Image) StripMetadata() error {
	if err := im.ref.RemoveMetadata(); err != nil {
		return fmt.Errorf("remove metadata: %w", err)
	}
	if err := im.ref.RemoveICCProfile(); err != nil {
		return fmt.Errorf("remove icc profile: %w", err)
	}
	return nil
}

// Watermark overlays text in the bottom-right corner. The label is sized
// relative to the current image, so it stays proportionate at any resolution.
// An empty text is a no-op.
func (im *Image) Watermark(text string) error {
	if text == "" {
		return nil
	}

	w, h := im.ref.Width(), im.ref.Height()
	boxW, boxH, marginX, marginY := watermarkBox(w, h)

	params := &vips.LabelParams{
		Text:      text,
		Font:      im.cfg.fontName,
		Width:     vips.ValueOf(float64(boxW)),
		Height:    vips.ValueOf(float64(boxH)),
		OffsetX:   vips.ValueOf(float64(w - boxW - marginX)),
		OffsetY:   vips.ValueOf(float64(h - boxH - marginY)),
		Opacity:   watermarkOpacity,
		Color:     vips.Color{R: 255, G: 255, B: 255},
		Alignment: vips.AlignHigh,
	}
	if err := im.ref.Label(params); err != nil {
		return fmt.Errorf("label %q: %w", text, err)
	}
	return nil
}

// ConvertFormat encodes the image and returns the encoded bytes.
//
// quality is a 1-100 setting for the lossy formats; zero selects the encoder
// default. It is ignored for PNG, which is lossless.
func (im *Image) ConvertFormat(format string, quality int) ([]byte, error) {
	f, err := normalizeFormat(format)
	if err != nil {
		return nil, err
	}

	var encoded []byte
	switch f {
	case domain.FormatJPEG:
		params := vips.NewJpegExportParams()
		if quality > 0 {
			params.Quality = quality
		}
		encoded, _, err = im.ref.ExportJpeg(params)
	case domain.FormatPNG:
		encoded, _, err = im.ref.ExportPng(vips.NewPngExportParams())
	case domain.FormatWebP:
		params := vips.NewWebpExportParams()
		if quality > 0 {
			params.Quality = quality
		}
		encoded, _, err = im.ref.ExportWebp(params)
	case domain.FormatAVIF:
		params := vips.NewAvifExportParams()
		if quality > 0 {
			params.Quality = quality
		}
		encoded, _, err = im.ref.ExportAvif(params)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedFormat, format)
	}

	if err != nil {
		return nil, fmt.Errorf("export %s: %w", f, err)
	}
	return encoded, nil
}
