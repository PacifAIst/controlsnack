// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package x11

import (
	"fmt"
	"image"

	"github.com/jezek/xgb/xproto"
)

// ErrUnsupportedFormat means the X server's screen is not in the one pixel format the agent reads: 32 bits per pixel,
// 24-bit true color (red 0xff0000, green 0xff00, blue 0xff), least significant byte first. Xorg gives the guest
// exactly that; anything else would need a conversion nobody has asked for.
type ErrUnsupportedFormat struct{ Reason string }

func (e *ErrUnsupportedFormat) Error() string { return "unsupported screen format: " + e.Reason }

// checkFormat verifies that the root window's pixels can be read as BGRX.
func checkFormat(setup *xproto.SetupInfo, scr *xproto.ScreenInfo) error {
	bad := func(format string, args ...any) error {
		return &ErrUnsupportedFormat{Reason: fmt.Sprintf(format, args...)}
	}
	if setup.ImageByteOrder != xproto.ImageOrderLSBFirst {
		return bad("the server sends the most significant byte first")
	}
	bpp := -1
	for _, f := range setup.PixmapFormats {
		if f.Depth == scr.RootDepth {
			bpp = int(f.BitsPerPixel)
		}
	}
	if bpp != 32 {
		return bad("depth %d is stored as %d bits per pixel, not 32", scr.RootDepth, bpp)
	}
	for _, d := range scr.AllowedDepths {
		if d.Depth != scr.RootDepth {
			continue
		}
		for _, v := range d.Visuals {
			if v.VisualId != scr.RootVisual {
				continue
			}
			if v.Class != xproto.VisualClassTrueColor && v.Class != xproto.VisualClassDirectColor {
				return bad("the root visual is not true color (class %d)", v.Class)
			}
			if v.RedMask != 0xff0000 || v.GreenMask != 0xff00 || v.BlueMask != 0xff {
				return bad("channel masks are %#x, %#x, %#x", v.RedMask, v.GreenMask, v.BlueMask)
			}
			return nil
		}
	}
	return bad("the root visual %d is not listed", scr.RootVisual)
}

// bgrxToRGBA converts rows of w*4 bytes in the order blue, green, red, unused into dst, which must be w by h. The
// unused byte is set to opaque: X leaves it undefined, and an encoder would take it for transparency.
func bgrxToRGBA(dst *image.RGBA, src []byte, w, h int) error {
	if len(src) < w*h*4 {
		return fmt.Errorf("the screen image has %d bytes, a %dx%d screen needs %d", len(src), w, h, w*h*4)
	}
	if dst.Bounds().Dx() != w || dst.Bounds().Dy() != h {
		return fmt.Errorf("converting into a %v image, the screen is %dx%d", dst.Bounds(), w, h)
	}
	for y := 0; y < h; y++ {
		s := src[y*w*4 : (y+1)*w*4]
		d := dst.Pix[y*dst.Stride : y*dst.Stride+w*4]
		for x := 0; x+4 <= len(s) && x+4 <= len(d); x += 4 {
			p, q := s[x:x+4:x+4], d[x:x+4:x+4] // four-byte windows: the compiler can see that the indexes below are in range
			q[0], q[1], q[2], q[3] = p[2], p[1], p[0], 0xff
		}
	}
	return nil
}
