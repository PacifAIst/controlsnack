// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package x11

import (
	"context"
	"errors"
	"io"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/jezek/xgb/xproto"
)

// These tests need a running X server: `xvfb-run -a -s "-screen 0 1280x1024x24 -noreset" go test -tags integration ./internal/agent/...`
// (the CI job of PLAN.md section 13.5). Without DISPLAY they skip. Xvfb resets itself when its last client goes unless it is given
// -noreset, and a client that comes in that moment is turned away (EOF, or connection reset by peer), so a connection that fails
// that way is tried again for a moment.

func openOrSkip(t *testing.T) *Conn {
	t.Helper()
	if os.Getenv("DISPLAY") == "" {
		t.Skip("no X display")
	}
	var c *Conn
	var err error
	for deadline := time.Now().Add(3 * time.Second); ; {
		c, err = Open("")
		if err == nil || time.Now().After(deadline) || !(errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNRESET)) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		var uf *ErrUnsupportedFormat
		if asUnsupported(err, &uf) {
			t.Skipf("this X server's pixel format is not the one the agent reads: %v", err)
		}
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func asUnsupported(err error, target **ErrUnsupportedFormat) bool { return errors.As(err, target) }

func TestCaptureOnARealServer(t *testing.T) {
	c := openOrSkip(t)
	w, h, err := c.Size()
	if err != nil || w <= 0 || h <= 0 {
		t.Fatalf("%d x %d %v", w, h, err)
	}
	img, err := c.Capture(context.Background())
	skipIfRootUnreadable(t, err)
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != w || img.Bounds().Dy() != h {
		t.Errorf("the capture is %v, the screen %dx%d", img.Bounds(), w, h)
	}
	for i := 3; i < len(img.Pix); i += 4 * 997 { // a sample of the alpha bytes
		if img.Pix[i] != 0xff {
			t.Fatalf("alpha byte %d is %#x: the unused X byte must be made opaque", i, img.Pix[i])
		}
	}
	// Every capture is a picture of its own: the screenshot, input, text recognition and waiting workers read the screen
	// at the same time.
	again, err := c.Capture(context.Background())
	if err != nil || again == img || &again.Pix[0] == &img.Pix[0] {
		t.Errorf("a second capture must not share pixels with the first: %v", err)
	}
}

func TestPointerOnARealServer(t *testing.T) {
	c := openOrSkip(t)
	w, h, _ := c.Size()
	x, y, err := c.Pointer()
	if err != nil || x < 0 || y < 0 || x >= w || y >= h {
		t.Errorf("(%d, %d) on a %dx%d screen: %v", x, y, w, h, err)
	}
}

func TestCaptureHonorsACanceledContext(t *testing.T) {
	c := openOrSkip(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Capture(ctx); err == nil {
		t.Error("a canceled request must not read the screen")
	}
}

// skipIfRootUnreadable skips the test when the server refuses to hand out the root window's pixels. XWayland (WSLg and
// other Wayland desktops) does: the compositor does not let a client read the whole screen. The guest runs Xorg, and
// the CI job runs Xvfb, where reading works.
func skipIfRootUnreadable(t *testing.T, err error) {
	t.Helper()
	var match xproto.MatchError
	if errors.As(err, &match) {
		t.Skipf("this X server does not let clients read the root window (XWayland?): %v", err)
	}
}
