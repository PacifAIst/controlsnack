// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package x11

import (
	"context"
	"errors"
	"fmt"
	"image"
	"sync"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

// Conn is the agent's connection to the X server. It is safe for concurrent use; the capture buffer is shared, so
// the image Capture returns is valid until the next call.
type Conn struct {
	c    *xgb.Conn
	root xproto.Window

	mu  sync.Mutex
	buf *image.RGBA

	atomMu sync.Mutex
	atoms  map[string]xproto.Atom

	randrOnce sync.Once
	randr15   bool
}

// Open connects to an X display (":0"; an empty string means $DISPLAY) and checks that its screen can be read.
func Open(display string) (*Conn, error) {
	xc, err := xgb.NewConnDisplay(display)
	if err != nil {
		return nil, fmt.Errorf("connecting to the X server: %w", err)
	}
	setup := xproto.Setup(xc)
	if len(setup.Roots) == 0 {
		xc.Close()
		return nil, errors.New("the X server reports no screen")
	}
	scr := &setup.Roots[xc.DefaultScreen]
	if err := checkFormat(setup, scr); err != nil {
		xc.Close()
		return nil, err
	}
	return &Conn{c: xc, root: scr.Root, atoms: map[string]xproto.Atom{}}, nil
}

// Close ends the connection.
func (c *Conn) Close() { c.c.Close() }

// Size is the current size of the screen; it follows resizes.
func (c *Conn) Size() (w, h int, err error) {
	g, err := xproto.GetGeometry(c.c, xproto.Drawable(c.root)).Reply()
	if err != nil {
		return 0, 0, fmt.Errorf("reading the screen size: %w", err)
	}
	return int(g.Width), int(g.Height), nil
}

// Capture reads the whole screen. The returned image is reused by the next call.
func (c *Conn) Capture(ctx context.Context) (*image.RGBA, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	w, h, err := c.Size()
	if err != nil {
		return nil, err
	}
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("the screen is %dx%d", w, h)
	}
	if w > 0xffff || h > 0xffff { // the protocol counts pixels in 16 bits
		return nil, fmt.Errorf("the screen is %dx%d, larger than X can hand out in one image", w, h)
	}
	reply, err := xproto.GetImage(c.c, xproto.ImageFormatZPixmap, xproto.Drawable(c.root), 0, 0, uint16(w), uint16(h), 0xffffffff).Reply() //nolint:gosec // checked above
	if err != nil {
		return nil, fmt.Errorf("reading the screen: %w", err)
	}
	if c.buf == nil || c.buf.Bounds().Dx() != w || c.buf.Bounds().Dy() != h {
		c.buf = image.NewRGBA(image.Rect(0, 0, w, h))
	}
	if err := bgrxToRGBA(c.buf, reply.Data, w, h); err != nil {
		return nil, err
	}
	return c.buf, nil
}

// Pointer is where the mouse pointer is, in screen pixels.
func (c *Conn) Pointer() (x, y int, err error) {
	p, err := xproto.QueryPointer(c.c, c.root).Reply()
	if err != nil {
		return 0, 0, fmt.Errorf("reading the pointer position: %w", err)
	}
	return int(p.RootX), int(p.RootY), nil
}
