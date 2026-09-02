//go:build nogovips

package imageproc

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"

	// Register the decoders for the formats this backend can read.
	_ "image/gif"

	// WebP is decode-only in pure Go; there is no encoder to register.
	_ "golang.org/x/image/webp"

	"imageforge/internal/domain"
)

// Backend names the compiled-in implementation.
const Backend = "purego"

// defaultJPEGQuality matches image/jpeg's own default, used when the caller
// does not ask for a specific quality.
const defaultJPEGQuality = 75

// watermarkAlpha is the alpha applied to the watermark text, as an 8-bit value
// (0.65 of full opacity, rounded).
const watermarkAlpha uint8 = 166

// Processor transforms images with the Go standard library and
// golang.org/x/image, for environments without libvips.
//
// It reads JPEG, PNG, GIF and WebP, and writes JPEG and PNG. Requesting WebP or
// AVIF output returns ErrUnsupportedFormat: there is no pure-Go encoder for
// either. A Processor is safe for concurrent use.
type Processor struct {
	cfg config
}

// New returns a Processor. It never fails, and returns an error only to match
// the signature of the govips backend.
func New(opts ...Option) (*Processor, error) {
	return &Processor{cfg: newConfig(opts...)}, nil
}

// Shutdown is a no-op: this backend holds no process-wide resources. It exists
// so that callers can be written against either backend.
func Shutdown() {}

// Image is a decoded image being transformed.
type Image struct {
	cfg config
	img image.Image
}

// Open decodes the image read from r.
func (p *Processor) Open(r io.Reader) (*Image, error) {
	img, _, err := image.Decode(r)
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if img.Bounds().Empty() {
		return nil, fmt.Errorf("decode: %w", ErrEmptySource)
	}
	return &Image{cfg: p.cfg, img: img}, nil
}

// Close releases the decoded image. It is safe to call more than once.
func (im *Image) Close() { im.img = nil }

// Width returns the current width in pixels.
func (im *Image) Width() int { return im.img.Bounds().Dx() }

// Height returns the current height in pixels.
func (im *Image) Height() int { return im.img.Bounds().Dy() }

// Resize scales the image to width x height using a Catmull-Rom kernel.
//
// A non-positive width or height is derived from the other, preserving the
// source aspect ratio. When both are given, keepAspect decides between fitting
// the image inside that box and stretching it to exactly those dimensions.
func (im *Image) Resize(width, height int, keepAspect bool) error {
	bounds := im.img.Bounds()

	targetW, targetH, err := targetSize(bounds.Dx(), bounds.Dy(), width, height, keepAspect)
	if err != nil {
		return err
	}
	if targetW == bounds.Dx() && targetH == bounds.Dy() {
		return nil
	}

	im.img = scale(im.img, targetW, targetH)
	return nil
}

// GenerateThumbnail scales the image down so its longest side is maxDim,
// preserving the aspect ratio. Images already within the bound are left
// untouched: a thumbnail never upscales.
func (im *Image) GenerateThumbnail(maxDim int) error {
	bounds := im.img.Bounds()

	targetW, targetH, err := thumbnailSize(bounds.Dx(), bounds.Dy(), maxDim)
	if err != nil {
		return err
	}
	if targetW == bounds.Dx() && targetH == bounds.Dy() {
		return nil
	}

	im.img = scale(im.img, targetW, targetH)
	return nil
}

// StripMetadata is a no-op for this backend.
//
// Decoding discards everything but the pixels, and the standard library
// encoders write no EXIF, XMP, IPTC or ICC data, so any metadata on the source
// is already gone by the time the image is re-encoded.
func (im *Image) StripMetadata() error { return nil }

// Watermark overlays text in the bottom-right corner using the built-in 7x13
// bitmap face. An empty text is a no-op.
//
// Unlike the govips backend the glyphs are a fixed pixel size, so the text does
// not grow with the image, and an image too small to hold the text is an error
// rather than a silently missing watermark.
func (im *Image) Watermark(text string) error {
	if text == "" {
		return nil
	}

	bounds := im.img.Bounds()
	face := basicfont.Face7x13

	advance := font.MeasureString(face, text)
	_, _, marginX, marginY := watermarkBox(bounds.Dx(), bounds.Dy())

	textW := advance.Ceil()
	if textW+2*marginX > bounds.Dx() || face.Height+2*marginY > bounds.Dy() {
		return fmt.Errorf("watermark %q needs %dx%d pixels, image is %dx%d",
			text, textW, face.Height, bounds.Dx(), bounds.Dy())
	}

	dst := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(dst, dst.Bounds(), im.img, bounds.Min, draw.Src)

	drawer := &font.Drawer{
		Dst:  dst,
		Src:  image.NewUniform(color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: watermarkAlpha}),
		Face: face,
		Dot: fixed.Point26_6{
			X: fixed.I(dst.Bounds().Dx()-marginX) - advance,
			Y: fixed.I(dst.Bounds().Dy() - marginY - face.Descent),
		},
	}
	drawer.DrawString(text)

	im.img = dst
	return nil
}

// ConvertFormat encodes the image and returns the encoded bytes.
//
// Only JPEG and PNG are supported; WebP and AVIF return ErrUnsupportedFormat
// because no pure-Go encoder exists for them. quality is a 1-100 setting for
// JPEG and is ignored for PNG, which is lossless.
func (im *Image) ConvertFormat(format string, quality int) ([]byte, error) {
	f, err := normalizeFormat(format)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	switch f {
	case domain.FormatJPEG:
		if quality <= 0 {
			quality = defaultJPEGQuality
		}
		err = jpeg.Encode(&buf, im.img, &jpeg.Options{Quality: quality})
	case domain.FormatPNG:
		encoder := png.Encoder{CompressionLevel: png.DefaultCompression}
		err = encoder.Encode(&buf, im.img)
	case domain.FormatWebP, domain.FormatAVIF:
		return nil, fmt.Errorf("%w: %s cannot be encoded by the %s backend", ErrUnsupportedFormat, f, Backend)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedFormat, format)
	}

	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", f, err)
	}
	return buf.Bytes(), nil
}

// scale resamples src to width x height.
func scale(src image.Image, width, height int) image.Image {
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), xdraw.Src, nil)
	return dst
}
