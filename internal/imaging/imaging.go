// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package imaging

import (
	"bytes"
	"errors"
	"fmt"
	"hash/maphash"
	"image"
	"image/jpeg"
	"image/png"
	"strings"

	xdraw "golang.org/x/image/draw"
)

// Format is an image encoding the protocol knows (PLAN.md section 5.1).
type Format string

// The formats a screenshot can be asked for.
const (
	WebP Format = "webp"
	JPEG Format = "jpeg"
	PNG  Format = "png"
)

// Quality bounds; DefaultQuality is what D-11 settled on for WebP and JPEG.
const (
	DefaultQuality = 70
	MinQuality     = 1
	MaxQuality     = 100
)

// ParseFormat reads a format name. An empty string means WebP, "jpg" is JPEG.
func ParseFormat(s string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "webp":
		return WebP, nil
	case "jpeg", "jpg":
		return JPEG, nil
	case "png":
		return PNG, nil
	}
	return "", fmt.Errorf("%q is not an image format (webp, jpeg or png)", s)
}

// MIME is the media type of the format.
func (f Format) MIME() string {
	switch f {
	case WebP:
		return "image/webp"
	case JPEG:
		return "image/jpeg"
	case PNG:
		return "image/png"
	}
	return "application/octet-stream"
}

// ErrBadQuality means a quality outside MinQuality..MaxQuality.
var ErrBadQuality = errors.New("image quality must be between 1 and 100")

// Encode compresses an image. Quality applies to WebP and JPEG (0 means DefaultQuality); PNG is always lossless.
func Encode(img image.Image, f Format, quality int) ([]byte, error) {
	if quality == 0 {
		quality = DefaultQuality
	}
	if quality < MinQuality || quality > MaxQuality {
		return nil, ErrBadQuality
	}
	var buf bytes.Buffer
	switch f {
	case WebP:
		return encodeWebP(img, quality)
	case JPEG:
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
			return nil, err
		}
	case PNG:
		enc := png.Encoder{CompressionLevel: png.BestSpeed} // a screenshot is sent often: speed beats the last few percent
		if err := enc.Encode(&buf, img); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("cannot encode %q", f)
	}
	return buf.Bytes(), nil
}

// Scale returns src resized so that its longer edge is at most maxLongEdge pixels, and the factor between the two:
// image pixels per source pixel (1 when nothing changed). It never enlarges, and keeps the aspect ratio. A maxLongEdge
// of 0 or less means "as it is".
func Scale(src *image.RGBA, maxLongEdge int) (*image.RGBA, float64) {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	long := max(w, h)
	if maxLongEdge <= 0 || long <= maxLongEdge {
		return src, 1
	}
	f := float64(maxLongEdge) / float64(long)
	nw, nh := max(1, int(float64(w)*f+0.5)), max(1, int(float64(h)*f+0.5))
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	xdraw.BiLinear.Scale(dst, dst.Bounds(), src, b, xdraw.Src, nil)
	return dst, f
}

// Hasher fingerprints screen contents so that "has the screen changed?" is a comparison of two short strings. The
// seed is random per process: fingerprints are only comparable within one agent.
type Hasher struct{ seed maphash.Seed }

// NewHasher makes a Hasher with a fresh seed.
func NewHasher() Hasher { return Hasher{seed: maphash.MakeSeed()} }

// Hash returns 16 hex digits over the size and every pixel. Rows are hashed one by one, so what lies between them in
// the buffer (the stride) does not matter, and a sub-image hashes like a copy of it.
func (h Hasher) Hash(img *image.RGBA) string {
	var mh maphash.Hash
	mh.SetSeed(h.seed)
	b := img.Bounds()
	_, _ = fmt.Fprintf(&mh, "%dx%d:", b.Dx(), b.Dy())
	rowLen := b.Dx() * 4
	for y := b.Min.Y; y < b.Max.Y; y++ {
		off := img.PixOffset(b.Min.X, y)
		_, _ = mh.Write(img.Pix[off : off+rowLen])
	}
	return fmt.Sprintf("%016x", mh.Sum64())
}
