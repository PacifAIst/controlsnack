// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package x11

import (
	"errors"
	"image"
	"strings"
	"testing"

	"github.com/jezek/xgb/xproto"
)

// xorgSetup is what an Xorg screen with the usual 24-bit true color visual announces.
func xorgSetup() (*xproto.SetupInfo, *xproto.ScreenInfo) {
	setup := &xproto.SetupInfo{
		ImageByteOrder: xproto.ImageOrderLSBFirst,
		PixmapFormats: []xproto.Format{
			{Depth: 1, BitsPerPixel: 1, ScanlinePad: 32},
			{Depth: 24, BitsPerPixel: 32, ScanlinePad: 32},
			{Depth: 32, BitsPerPixel: 32, ScanlinePad: 32},
		},
	}
	scr := &xproto.ScreenInfo{
		RootDepth: 24, RootVisual: 0x21,
		AllowedDepths: []xproto.DepthInfo{
			{Depth: 32, Visuals: []xproto.VisualInfo{{VisualId: 0x99, Class: xproto.VisualClassTrueColor, RedMask: 0xff0000, GreenMask: 0xff00, BlueMask: 0xff}}},
			{Depth: 24, Visuals: []xproto.VisualInfo{
				{VisualId: 0x20, Class: xproto.VisualClassStaticGray},
				{VisualId: 0x21, Class: xproto.VisualClassTrueColor, RedMask: 0xff0000, GreenMask: 0xff00, BlueMask: 0xff},
			}},
		},
	}
	return setup, scr
}

func TestCheckFormatAcceptsWhatXorgGives(t *testing.T) {
	setup, scr := xorgSetup()
	if err := checkFormat(setup, scr); err != nil {
		t.Fatal(err)
	}
	scr.AllowedDepths[1].Visuals[1].Class = xproto.VisualClassDirectColor // also readable
	if err := checkFormat(setup, scr); err != nil {
		t.Errorf("direct color with the same masks: %v", err)
	}
}

func TestCheckFormatRefusesWhatItCannotRead(t *testing.T) {
	tests := []struct {
		name string
		mut  func(*xproto.SetupInfo, *xproto.ScreenInfo)
		want string
	}{
		{"big endian server", func(s *xproto.SetupInfo, _ *xproto.ScreenInfo) { s.ImageByteOrder = xproto.ImageOrderMSBFirst }, "most significant byte first"},
		{"16 bits per pixel", func(s *xproto.SetupInfo, _ *xproto.ScreenInfo) { s.PixmapFormats[1].BitsPerPixel = 16 }, "not 32"},
		{"24 bits per pixel", func(s *xproto.SetupInfo, _ *xproto.ScreenInfo) { s.PixmapFormats[1].BitsPerPixel = 24 }, "not 32"},
		{"no pixmap format for the depth", func(s *xproto.SetupInfo, sc *xproto.ScreenInfo) { sc.RootDepth = 16 }, "not 32"},
		{"swapped red and blue", func(_ *xproto.SetupInfo, sc *xproto.ScreenInfo) {
			sc.AllowedDepths[1].Visuals[1].RedMask, sc.AllowedDepths[1].Visuals[1].BlueMask = 0xff, 0xff0000
		}, "channel masks"},
		{"palette visual", func(_ *xproto.SetupInfo, sc *xproto.ScreenInfo) {
			sc.AllowedDepths[1].Visuals[1].Class = xproto.VisualClassPseudoColor
		}, "not true color"},
		{"unlisted visual", func(_ *xproto.SetupInfo, sc *xproto.ScreenInfo) { sc.RootVisual = 0x77 }, "not listed"},
		{"no visuals at the root depth", func(_ *xproto.SetupInfo, sc *xproto.ScreenInfo) { sc.AllowedDepths = sc.AllowedDepths[:1] }, "not listed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setup, scr := xorgSetup()
			tt.mut(setup, scr)
			err := checkFormat(setup, scr)
			var uf *ErrUnsupportedFormat
			if !errors.As(err, &uf) || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("%v", err)
			}
		})
	}
}

func TestBGRXToRGBA(t *testing.T) {
	// Two pixels in a 2x2 screen: blue-green-red order, the fourth byte is garbage that must not become transparency.
	src := []byte{
		10, 20, 30, 0x00, 40, 50, 60, 0x7f,
		70, 80, 90, 0x11, 255, 0, 128, 0xee,
	}
	dst := image.NewRGBA(image.Rect(0, 0, 2, 2))
	if err := bgrxToRGBA(dst, src, 2, 2); err != nil {
		t.Fatal(err)
	}
	want := []byte{
		30, 20, 10, 255, 60, 50, 40, 255,
		90, 80, 70, 255, 128, 0, 255, 255,
	}
	for i := range want {
		if dst.Pix[i] != want[i] {
			t.Fatalf("byte %d: %d, want %d\n got %v\nwant %v", i, dst.Pix[i], want[i], dst.Pix, want)
		}
	}
}

func TestBGRXToRGBAChecksSizes(t *testing.T) {
	dst := image.NewRGBA(image.Rect(0, 0, 4, 4))
	if err := bgrxToRGBA(dst, make([]byte, 4*4*4-1), 4, 4); err == nil {
		t.Error("a short image is an error, not a crash")
	}
	if err := bgrxToRGBA(dst, make([]byte, 100), 3, 3); err == nil {
		t.Error("a buffer of the wrong size")
	}
	if err := bgrxToRGBA(dst, make([]byte, 4*4*4+64), 4, 4); err != nil {
		t.Errorf("extra bytes after the picture are ignored: %v", err)
	}
	// A destination with a stride wider than the picture (a sub-image) is filled row by row.
	wide := image.NewRGBA(image.Rect(0, 0, 8, 2))
	sub := wide.SubImage(image.Rect(0, 0, 2, 2)).(*image.RGBA)
	src := []byte{1, 2, 3, 0, 4, 5, 6, 0, 7, 8, 9, 0, 10, 11, 12, 0}
	if err := bgrxToRGBA(sub, src, 2, 2); err != nil {
		t.Fatal(err)
	}
	if wide.Pix[0] != 3 || wide.Pix[wide.Stride] != 9 || wide.Pix[wide.Stride+4] != 12 || wide.Pix[8] != 0 {
		t.Errorf("rows must land at the stride: %v", wide.Pix[:wide.Stride+8])
	}
}

func BenchmarkBGRXToRGBA1080p(b *testing.B) {
	src := make([]byte, 1920*1080*4)
	dst := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
	b.SetBytes(int64(len(src)))
	for i := 0; i < b.N; i++ {
		_ = bgrxToRGBA(dst, src, 1920, 1080)
	}
}
