// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package agent

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"

	"github.com/PacifAIst/controlsnack-local/internal/agent/server"
	"github.com/PacifAIst/controlsnack-local/internal/protocol"
)

type fakeScreen struct {
	img       *image.RGBA
	capErr    error
	ptrErr    error
	px, py    int
	captures  int
	onCapture func()
}

func (f *fakeScreen) Capture(context.Context) (*image.RGBA, error) {
	f.captures++
	if f.onCapture != nil {
		f.onCapture()
	}
	return f.img, f.capErr
}

func (f *fakeScreen) Pointer() (int, int, error) { return f.px, f.py, f.ptrErr }

func testScreen(w, h int) *fakeScreen {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{R: 20, G: 20, B: 24, A: 255}
			if y < 24 {
				c = color.RGBA{R: 235, G: 235, B: 235, A: 255}
			} else if (x/6+y/3)%7 == 0 {
				c = color.RGBA{R: 200, G: 210, B: 220, A: 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	return &fakeScreen{img: img, px: 12, py: 34}
}

func request(t *testing.T, body any) protocol.Frame {
	t.Helper()
	f, err := protocol.NewFrame(1, protocol.TypeScreenshot, body, nil)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func shoot(t *testing.T, s *Screenshots, body any) (protocol.ScreenshotRes, []byte, error) {
	t.Helper()
	res, blob, err := s.Handle(context.Background(), request(t, body))
	if err != nil {
		return protocol.ScreenshotRes{}, nil, err
	}
	return res.(protocol.ScreenshotRes), blob, nil
}

func code(err error) string {
	var he *server.HandlerError
	if errors.As(err, &he) {
		return he.Code
	}
	return ""
}

func TestScreenshotPNGIsTheScreen(t *testing.T) {
	scr := testScreen(320, 200)
	res, blob, err := shoot(t, NewScreenshots(scr), protocol.ScreenshotReq{Format: "png"})
	if err != nil {
		t.Fatal(err)
	}
	if res.W != 320 || res.H != 200 || res.Format != "png" || res.Scale != 1 || res.Unchanged || len(res.Hash) != 16 || res.Cursor != (protocol.Point{X: 12, Y: 34}) {
		t.Errorf("%+v", res)
	}
	got, err := png.Decode(bytes.NewReader(blob))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range [][2]int{{0, 0}, {5, 5}, {100, 100}, {319, 199}} {
		a := color.RGBAModel.Convert(scr.img.At(p[0], p[1]))
		b := color.RGBAModel.Convert(got.At(p[0], p[1]))
		if a != b {
			t.Errorf("pixel %v: %v, want %v", p, b, a)
		}
	}
}

func TestScreenshotJPEGAndScaling(t *testing.T) {
	scr := testScreen(1280, 800)
	res, blob, err := shoot(t, NewScreenshots(scr), protocol.ScreenshotReq{Format: "jpeg", Quality: 60, MaxLongEdge: 640})
	if err != nil {
		t.Fatal(err)
	}
	if res.W != 640 || res.H != 400 || res.Scale != 0.5 || res.Format != "jpeg" {
		t.Errorf("%+v", res)
	}
	got, err := jpeg.Decode(bytes.NewReader(blob))
	if err != nil || got.Bounds().Dx() != 640 || got.Bounds().Dy() != 400 {
		t.Errorf("the picture has the size the response says: %v %v", got.Bounds(), err)
	}
	// An image that already fits is not enlarged.
	res, _, err = shoot(t, NewScreenshots(scr), protocol.ScreenshotReq{Format: "jpeg", MaxLongEdge: 4000})
	if err != nil || res.W != 1280 || res.Scale != 1 {
		t.Errorf("%+v %v", res, err)
	}
}

func TestScreenshotChangeDetection(t *testing.T) {
	scr := testScreen(320, 200)
	s := NewScreenshots(scr)
	first, _, err := shoot(t, s, protocol.ScreenshotReq{Format: "png"})
	if err != nil {
		t.Fatal(err)
	}

	// Nothing changed: no picture, the same hash, and the cursor is still reported.
	scr.px = 99
	res, blob, err := shoot(t, s, protocol.ScreenshotReq{Format: "png", IfChangedSince: first.Hash})
	if err != nil || !res.Unchanged || res.Hash != first.Hash || len(blob) != 0 || res.Cursor.X != 99 {
		t.Errorf("%+v %d bytes %v", res, len(blob), err)
	}
	if res.W != 0 || res.Format != "" {
		t.Errorf("an unchanged answer describes no picture: %+v", res)
	}

	// One pixel changed: a new picture and a new hash.
	scr.img.SetRGBA(200, 150, color.RGBA{R: 255, A: 255})
	res, blob, err = shoot(t, s, protocol.ScreenshotReq{Format: "png", IfChangedSince: first.Hash})
	if err != nil || res.Unchanged || res.Hash == first.Hash || len(blob) == 0 {
		t.Errorf("%+v %d bytes %v", res, len(blob), err)
	}
	// Asking about a hash that is not the current one always gets a picture.
	res, blob, err = shoot(t, s, protocol.ScreenshotReq{Format: "png", IfChangedSince: "0123456789abcdef"})
	if err != nil || res.Unchanged || len(blob) == 0 {
		t.Errorf("%+v %v", res, err)
	}
	// The hash describes the screen, not the way it was asked for.
	a, _, _ := shoot(t, s, protocol.ScreenshotReq{Format: "png"})
	b, _, _ := shoot(t, s, protocol.ScreenshotReq{Format: "jpeg", MaxLongEdge: 100})
	if a.Hash != b.Hash {
		t.Error("the same screen has the same hash whatever the format or size")
	}
}

func TestScreenshotRefusesBadParameters(t *testing.T) {
	scr := testScreen(64, 64)
	s := NewScreenshots(scr)
	tests := []struct {
		name string
		body any
		code string
		want string
	}{
		{"unknown format", protocol.ScreenshotReq{Format: "gif"}, protocol.CodeInvalidArgs, "format"},
		{"negative quality", protocol.ScreenshotReq{Format: "jpeg", Quality: -1}, protocol.CodeInvalidArgs, "quality"},
		{"quality over 100", protocol.ScreenshotReq{Format: "jpeg", Quality: 101}, protocol.CodeInvalidArgs, "quality"},
		{"tiny long edge", protocol.ScreenshotReq{MaxLongEdge: 8}, protocol.CodeInvalidArgs, "max_long_edge"},
		{"huge long edge", protocol.ScreenshotReq{MaxLongEdge: 100000}, protocol.CodeInvalidArgs, "max_long_edge"},
		{"negative long edge", protocol.ScreenshotReq{MaxLongEdge: -1}, protocol.CodeInvalidArgs, "max_long_edge"},
		{"a hash that is not one", protocol.ScreenshotReq{IfChangedSince: "abc"}, protocol.CodeInvalidArgs, "if_changed_since"},
		{"upper case hash", protocol.ScreenshotReq{IfChangedSince: "0123456789ABCDEF"}, protocol.CodeInvalidArgs, "if_changed_since"},
		{"marks without a11y", protocol.ScreenshotReq{MarksGeneration: "g1"}, protocol.CodeUnsupported, "accessibility"},
		{"unknown field", map[string]any{"format": "png", "sudo": true}, protocol.CodeInvalidArgs, "unknown field"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := s.Handle(context.Background(), request(t, tt.body))
			var he *server.HandlerError
			if !errors.As(err, &he) || he.Code != tt.code || !strings.Contains(he.Message, tt.want) {
				t.Errorf("%v", err)
			}
		})
	}
	if scr.captures != 0 {
		t.Error("a request that is refused must not touch the screen")
	}
}

func TestScreenshotCaptureFailure(t *testing.T) {
	scr := testScreen(64, 64)
	scr.capErr = errors.New("BadMatch")
	_, _, err := NewScreenshots(scr).Handle(context.Background(), request(t, protocol.ScreenshotReq{Format: "png"}))
	if code(err) != protocol.CodeInternal || !strings.Contains(err.Error(), "BadMatch") {
		t.Errorf("%v", err)
	}
}

func TestScreenshotCursorIsOptional(t *testing.T) {
	scr := testScreen(64, 64)
	scr.ptrErr = errors.New("no pointer")
	res, _, err := shoot(t, NewScreenshots(scr), protocol.ScreenshotReq{Format: "png"})
	if err != nil || res.Cursor != (protocol.Point{}) {
		t.Errorf("a screenshot does not fail because the pointer cannot be read: %+v %v", res, err)
	}
}

func TestScreenshotStopsWhenCanceled(t *testing.T) {
	scr := testScreen(640, 400)
	ctx, cancel := context.WithCancel(context.Background())
	scr.onCapture = cancel // the host gives up while the screen is being read
	_, _, err := NewScreenshots(scr).Handle(ctx, request(t, protocol.ScreenshotReq{Format: "png"}))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("%v", err)
	}
}

func TestScreenshotTooLargeForOneFrame(t *testing.T) {
	// A noisy 2600x2600 picture does not fit a PNG into the 8 MiB blob limit.
	img := image.NewRGBA(image.Rect(0, 0, 2600, 2600))
	x := uint32(1)
	for i := range img.Pix {
		x = x*1664525 + 1013904223
		img.Pix[i] = byte(x >> 24)
	}
	_, _, err := NewScreenshots(&fakeScreen{img: img}).Handle(context.Background(), request(t, protocol.ScreenshotReq{Format: "png"}))
	if code(err) != protocol.CodeUnsupported || !strings.Contains(err.Error(), "max_long_edge") {
		t.Errorf("%v", err)
	}
}

func TestSize(t *testing.T) {
	if size(500<<10) != "500 KiB" || size(3<<20+(1<<19)) != "3.5 MiB" {
		t.Errorf("%q %q", size(500<<10), size(3<<20+(1<<19)))
	}
}
