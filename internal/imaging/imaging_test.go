// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package imaging

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"strings"
	"sync"
	"testing"

	xwebp "golang.org/x/image/webp"
)

// screenLike draws something that resembles a screen: a dark background, a title bar, text-like stripes and a
// gradient, so compression and scaling are tested on edges and not on a flat color.
func screenLike(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{R: 30, G: 30, B: 34, A: 255}
			switch {
			case y < h/20:
				c = color.RGBA{R: 240, G: 240, B: 240, A: 255}
			case y%14 < 2 && x > w/10 && x < w*9/10 && (x/7)%3 != 0:
				c = color.RGBA{R: 220, G: 220, B: 220, A: 255}
			case x > w*6/10 && y > h/2:
				c = color.RGBA{R: uint8(x * 255 / w), G: uint8(y * 255 / h), B: 120, A: 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

func psnr(a, b image.Image) float64 {
	r := a.Bounds()
	var sum float64
	n := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			r1, g1, b1, _ := a.At(x, y).RGBA()
			r2, g2, b2, _ := b.At(x, y).RGBA()
			for _, d := range [3]float64{float64(r1>>8) - float64(r2>>8), float64(g1>>8) - float64(g2>>8), float64(b1>>8) - float64(b2>>8)} {
				sum += d * d
			}
			n += 3
		}
	}
	if sum == 0 {
		return 99
	}
	return 10 * math.Log10(255*255/(sum/float64(n)))
}

func TestParseFormat(t *testing.T) {
	for in, want := range map[string]Format{"": WebP, "webp": WebP, "WEBP": WebP, " webp ": WebP, "jpeg": JPEG, "jpg": JPEG, "JPG": JPEG, "png": PNG, "PNG": PNG} {
		if got, err := ParseFormat(in); err != nil || got != want {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
	for _, bad := range []string{"gif", "bmp", "webp2", "image/png", "avif", "p ng"} {
		if _, err := ParseFormat(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if WebP.MIME() != "image/webp" || JPEG.MIME() != "image/jpeg" || PNG.MIME() != "image/png" || Format("x").MIME() == "" {
		t.Error("media types")
	}
}

func TestEncodePNGIsLossless(t *testing.T) {
	src := screenLike(320, 200)
	data, err := Encode(src, PNG, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if psnr(src, got) != 99 {
		t.Error("a PNG screenshot must be the screen, bit for bit")
	}
}

func TestEncodeJPEG(t *testing.T) {
	src := screenLike(640, 400)
	data, err := Encode(src, JPEG, 70)
	if err != nil {
		t.Fatal(err)
	}
	got, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil || got.Bounds() != src.Bounds() {
		t.Fatalf("%v %v", got.Bounds(), err)
	}
	if p := psnr(src, got); p < 28 {
		t.Errorf("PSNR %.1f dB at quality 70", p)
	}
	low, _ := Encode(src, JPEG, 10)
	high, _ := Encode(src, JPEG, 95)
	if len(low) >= len(data) || len(data) >= len(high) {
		t.Errorf("a higher quality costs more bytes: %d < %d < %d", len(low), len(data), len(high))
	}
	def, _ := Encode(src, JPEG, 0)
	if !bytes.Equal(def, data) {
		t.Error("quality 0 means the default, 70")
	}
}

func TestEncodeRefusals(t *testing.T) {
	src := screenLike(64, 64)
	for _, q := range []int{-1, 101, 1000} {
		if _, err := Encode(src, JPEG, q); !errors.Is(err, ErrBadQuality) {
			t.Errorf("quality %d: %v", q, err)
		}
	}
	if _, err := Encode(src, Format("gif"), 70); err == nil {
		t.Error("unknown format")
	}
}

func TestScale(t *testing.T) {
	src := screenLike(1920, 1080)
	tests := []struct {
		name        string
		max         int
		w, h        int
		scale       float64
		sameAsInput bool
	}{
		{"half", 960, 960, 540, 0.5, false},
		{"to 1280", 1280, 1280, 720, 1280.0 / 1920, false},
		{"no change when it fits", 1920, 1920, 1080, 1, true},
		{"never enlarges", 4000, 1920, 1080, 1, true},
		{"zero means as it is", 0, 1920, 1080, 1, true},
		{"negative means as it is", -5, 1920, 1080, 1, true},
		{"tiny", 64, 64, 36, 64.0 / 1920, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, scale := Scale(src, tt.max)
			if got.Bounds().Dx() != tt.w || got.Bounds().Dy() != tt.h || math.Abs(scale-tt.scale) > 1e-9 {
				t.Errorf("%dx%d scale %v, want %dx%d scale %v", got.Bounds().Dx(), got.Bounds().Dy(), scale, tt.w, tt.h, tt.scale)
			}
			if (got == src) != tt.sameAsInput {
				t.Errorf("same buffer: %v", got == src)
			}
		})
	}
	// A portrait screen: the long edge is the height.
	port := screenLike(800, 1280)
	if got, scale := Scale(port, 640); got.Bounds().Dx() != 400 || got.Bounds().Dy() != 640 || math.Abs(scale-0.5) > 1e-9 {
		t.Errorf("%v %v", got.Bounds(), scale)
	}
	// An extreme aspect ratio never produces an empty image.
	thin := screenLike(4000, 3)
	if got, _ := Scale(thin, 100); got.Bounds().Dx() != 100 || got.Bounds().Dy() < 1 {
		t.Errorf("%v", got.Bounds())
	}
}

func TestScaleKeepsTheLook(t *testing.T) {
	src := screenLike(1280, 800)
	small, scale := Scale(src, 640)
	// Scaling back up is only for the comparison: a downscale that kept the picture is close to the original.
	back := image.NewRGBA(src.Bounds())
	for y := 0; y < 800; y++ {
		for x := 0; x < 1280; x++ {
			back.Set(x, y, small.At(min(int(float64(x)*scale), small.Bounds().Dx()-1), min(int(float64(y)*scale), small.Bounds().Dy()-1)))
		}
	}
	if p := psnr(src, back); p < 17 {
		t.Errorf("the downscaled picture does not look like the screen: PSNR %.1f", p)
	}
}

func TestHasher(t *testing.T) {
	h := NewHasher()
	a := screenLike(320, 200)
	b := screenLike(320, 200)
	if h.Hash(a) != h.Hash(b) {
		t.Error("equal screens, equal hashes")
	}
	if len(h.Hash(a)) != 16 {
		t.Errorf("16 hex digits: %q", h.Hash(a))
	}
	before := h.Hash(a)
	a.SetRGBA(17, 33, color.RGBA{R: 31, G: 30, B: 34, A: 255}) // one channel of one pixel
	if h.Hash(a) == before {
		t.Error("a single changed pixel must change the hash")
	}
	// The size is part of the fingerprint: the same bytes read as another shape are another screen.
	wide := &image.RGBA{Pix: make([]byte, 100*4), Stride: 100 * 4, Rect: image.Rect(0, 0, 100, 1)}
	tall := &image.RGBA{Pix: make([]byte, 100*4), Stride: 4, Rect: image.Rect(0, 0, 1, 100)}
	if h.Hash(wide) == h.Hash(tall) {
		t.Error("1x100 and 100x1 are different screens")
	}
	// Padding between rows does not matter, and a sub-image hashes like a copy of it.
	sub := a.SubImage(image.Rect(10, 10, 110, 90)).(*image.RGBA)
	cp := image.NewRGBA(image.Rect(0, 0, 100, 80))
	for y := 0; y < 80; y++ {
		copy(cp.Pix[y*cp.Stride:(y+1)*cp.Stride], a.Pix[a.PixOffset(10, 10+y):a.PixOffset(10, 10+y)+100*4])
	}
	if h.Hash(sub) != h.Hash(cp) {
		t.Error("a sub-image must hash like a copy of the same pixels")
	}
	// Another process, another seed: fingerprints are not comparable across hashers.
	if NewHasher().Hash(a) == h.Hash(a) {
		t.Error("two hashers produced the same fingerprint: the seed is not random")
	}
}

// --- WebP (D-11) ------------------------------------------------------------------------------------------------------

// decodeWebP decodes with the Go team's decoder, which is independent of the encoder. VP8 stores limited-range BT.601
// and x/image/webp returns the planes as image.YCbCr (full range), so the conversion to RGB is done here.
func decodeWebP(t *testing.T, data []byte) *image.RGBA {
	t.Helper()
	img, err := xwebp.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("an independent decoder rejects the output: %v", err)
	}
	b := img.Bounds()
	out := image.NewRGBA(b)
	if yc, ok := img.(*image.YCbCr); ok {
		clamp := func(v float64) uint8 { return uint8(math.Max(0, math.Min(255, v+0.5))) }
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				yy := 1.164383 * (float64(yc.Y[yc.YOffset(x, y)]) - 16)
				cb := float64(yc.Cb[yc.COffset(x, y)]) - 128
				cr := float64(yc.Cr[yc.COffset(x, y)]) - 128
				out.SetRGBA(x, y, color.RGBA{R: clamp(yy + 1.596027*cr), G: clamp(yy - 0.391762*cb - 0.812968*cr), B: clamp(yy + 2.017232*cb), A: 255})
			}
		}
		return out
	}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			out.Set(x, y, img.At(x, y))
		}
	}
	return out
}

func TestEncodeWebP(t *testing.T) {
	src := screenLike(1280, 800)
	data, err := Encode(src, WebP, 70)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		t.Fatalf("not a WebP container: %q", data[:min(16, len(data))])
	}
	if string(data[12:16]) != "VP8 " {
		t.Errorf("a lossy WebP carries a VP8 chunk, got %q", data[12:16])
	}
	got := decodeWebP(t, data)
	if got.Bounds() != src.Bounds() {
		t.Fatalf("%v", got.Bounds())
	}
	if p := psnr(src, got); p < 34 {
		t.Errorf("PSNR %.1f dB at quality 70", p)
	}
	if len(data) > 200<<10 {
		t.Errorf("a screen-like 1280x800 image takes %d bytes: the budget for a 1080p frame is 200 KB", len(data))
	}
}

func TestEncodeWebPQuality(t *testing.T) {
	src := screenLike(640, 400)
	low, err1 := Encode(src, WebP, 10)
	mid, err2 := Encode(src, WebP, 70)
	high, err3 := Encode(src, WebP, 95)
	if err1 != nil || err2 != nil || err3 != nil {
		t.Fatal(err1, err2, err3)
	}
	if len(low) >= len(mid) || len(mid) >= len(high) {
		t.Errorf("a higher quality costs more bytes: %d < %d < %d", len(low), len(mid), len(high))
	}
	if !(psnr(src, decodeWebP(t, low)) < psnr(src, decodeWebP(t, mid)) && psnr(src, decodeWebP(t, mid)) < psnr(src, decodeWebP(t, high))) {
		t.Error("a higher quality looks closer to the original")
	}
	def, _ := Encode(src, WebP, 0)
	if !bytes.Equal(def, mid) {
		t.Error("quality 0 means the default, 70")
	}
}

func TestEncodeWebPIsDeterministicAndSafeToShare(t *testing.T) {
	src := screenLike(320, 200)
	want, err := Encode(src, WebP, 70)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := Encode(src, WebP, 70)
			if err != nil || !bytes.Equal(got, want) {
				t.Errorf("concurrent encodes of the same image must agree: %v", err)
			}
		}()
	}
	wg.Wait()
}

func TestEncodeWebPOddSizes(t *testing.T) {
	// Odd widths and heights, and the smallest images, are where chroma subsampling code goes wrong.
	for _, size := range [][2]int{{1, 1}, {2, 3}, {15, 17}, {33, 1}, {1, 33}, {257, 129}} {
		src := screenLike(size[0], size[1])
		data, err := Encode(src, WebP, 70)
		if err != nil {
			t.Errorf("%dx%d: %v", size[0], size[1], err)
			continue
		}
		if got := decodeWebP(t, data); got.Bounds() != src.Bounds() {
			t.Errorf("%dx%d came back as %v", size[0], size[1], got.Bounds())
		}
	}
}

func TestInspect(t *testing.T) {
	src := screenLike(320, 200)
	for _, f := range []Format{WebP, JPEG, PNG} {
		data, err := Encode(src, f, 70)
		if err != nil {
			t.Fatal(err)
		}
		if err := Inspect(data, f, 320, 200); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	web, _ := Encode(src, WebP, 70)
	pngData, _ := Encode(src, PNG, 0)
	for _, tt := range []struct {
		name string
		data []byte
		f    Format
		w, h int
		want string
	}{
		{"wrong declared size", pngData, PNG, 640, 400, "declared a 640x400"},
		{"wrong declared format", pngData, JPEG, 320, 200, "declared a jpeg picture but sent a png"},
		{"webp declared as png", web, PNG, 320, 200, "but sent a webp"},
		{"not an image", []byte("GET / HTTP/1.1\r\n\r\n"), PNG, 1, 1, "not a readable"},
		{"empty", nil, WebP, 1, 1, "not a readable"},
		{"cut off header", pngData[:12], PNG, 320, 200, "not a readable"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := Inspect(tt.data, tt.f, tt.w, tt.h); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("%v", err)
			}
		})
	}
	// A file announcing more than MaxDimension is refused even when the declaration agrees with it.
	big := image.NewGray(image.Rect(0, 0, MaxDimension+1, 2))
	var buf bytes.Buffer
	if err := png.Encode(&buf, big); err != nil {
		t.Fatal(err)
	}
	if err := Inspect(buf.Bytes(), PNG, MaxDimension+1, 2); err == nil || !strings.Contains(err.Error(), "limit is 4096") {
		t.Errorf("%v", err)
	}
	tall := image.NewGray(image.Rect(0, 0, 2, MaxDimension))
	buf.Reset()
	_ = png.Encode(&buf, tall)
	if err := Inspect(buf.Bytes(), PNG, 2, MaxDimension); err != nil {
		t.Errorf("exactly the limit is fine: %v", err)
	}
}

// FuzzInspect: whatever the guest sends, reading the header never panics and never allocates the picture.
func FuzzInspect(f *testing.F) {
	src := screenLike(32, 24)
	for _, fm := range []Format{WebP, JPEG, PNG} {
		data, _ := Encode(src, fm, 70)
		f.Add(data, string(fm), 32, 24)
		f.Add(data[:len(data)/2], string(fm), 32, 24)
	}
	f.Add([]byte("RIFF\x00\x00\x00\x00WEBPVP8 "), "webp", 1, 1)
	f.Add([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\xff\xff\xff\xff\xff\xff\xff\xff"), "png", 1, 1)
	f.Fuzz(func(t *testing.T, data []byte, format string, w, h int) {
		_ = Inspect(data, Format(format), w, h)
	})
}
