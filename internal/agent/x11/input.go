// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package x11

import (
	"context"
	"fmt"

	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgb/xtest"
)

// Mouse buttons as X numbers them: 1 left, 2 middle, 3 right, 4 and 5 wheel up and down, 6 and 7 wheel left and right.
const (
	ButtonLeft = 1 + iota
	ButtonMiddle
	ButtonRight
	WheelUp
	WheelDown
	WheelLeft
	WheelRight
)

// drainEvents reads and drops the events the server sends this client. Nothing listens for them, but xgb stops reading
// from the socket when its event queue is full, and every keymap change the agent makes for typing sends one.
func (c *Conn) drainEvents() {
	for {
		ev, err := c.c.WaitForEvent()
		if ev == nil && err == nil { // the connection is closed
			return
		}
	}
}

// xtestOK enables the XTEST extension once.
func (c *Conn) xtestOK() error {
	c.xtestOnce.Do(func() { c.xtestErr = xtest.Init(c.c) })
	if c.xtestErr != nil {
		return fmt.Errorf("the X server has no XTEST extension, so input cannot be sent: %w", c.xtestErr)
	}
	return nil
}

// fakeInput sends one XTEST event and waits for the server to accept it, so that errors surface at once.
func (c *Conn) fakeInput(kind, detail byte, x, y int16) error {
	if err := c.xtestOK(); err != nil {
		return err
	}
	if err := xtest.FakeInputChecked(c.c, kind, detail, 0, c.root, x, y, 0).Check(); err != nil {
		return fmt.Errorf("sending input: %w", err)
	}
	return nil
}

// MoveTo puts the pointer at an absolute position on the screen.
func (c *Conn) MoveTo(x, y int) error {
	if x < 0 || y < 0 || x > 0x7fff || y > 0x7fff {
		return fmt.Errorf("pointer position (%d, %d) is out of range", x, y)
	}
	return c.fakeInput(xproto.MotionNotify, 0, int16(x), int16(y)) //nolint:gosec // range checked above
}

// Button presses or releases a mouse button (ButtonLeft ... WheelRight).
func (c *Conn) Button(button int, down bool) error {
	if button < ButtonLeft || button > WheelRight {
		return fmt.Errorf("mouse button %d does not exist", button)
	}
	kind := byte(xproto.ButtonRelease)
	if down {
		kind = xproto.ButtonPress
	}
	return c.fakeInput(kind, byte(button), 0, 0)
}

// fakeKey implements the keyboard's device on the real server.
func (c *Conn) fakeKey(code byte, down bool) error {
	kind := byte(xproto.KeyRelease)
	if down {
		kind = xproto.KeyPress
	}
	return c.fakeInput(kind, code, 0, 0)
}

// remap gives one keycode the same keysym in every column (0 clears it).
func (c *Conn) remap(code byte, per int, keysym uint32) error {
	syms := make([]xproto.Keysym, per)
	for i := range syms {
		syms[i] = xproto.Keysym(keysym)
	}
	if err := xproto.ChangeKeyboardMappingChecked(c.c, 1, xproto.Keycode(code), byte(per), syms).Check(); err != nil { //nolint:gosec // per comes from the server's own mapping
		return err
	}
	return nil
}

// sync waits until the server has processed everything sent so far.
func (c *Conn) sync() error {
	if _, err := xproto.GetInputFocus(c.c).Reply(); err != nil {
		return fmt.Errorf("waiting for the X server: %w", err)
	}
	return nil
}

// Sync is the exported form for callers that send pointer events.
func (c *Conn) Sync() error { return c.sync() }

// keymap reads the keyboard mapping as it is now. It is read again for every request that types, which is how changes
// (a layout switch, another client's remapping) are picked up without watching MappingNotify.
func (c *Conn) keymap() (Keymap, error) {
	setup := xproto.Setup(c.c)
	min, max := setup.MinKeycode, setup.MaxKeycode
	r, err := xproto.GetKeyboardMapping(c.c, min, byte(max-min+1)).Reply() //nolint:gosec // the range is the server's own
	if err != nil {
		return Keymap{}, fmt.Errorf("reading the keyboard mapping: %w", err)
	}
	syms := make([]uint32, len(r.Keysyms))
	for i, s := range r.Keysyms {
		syms[i] = uint32(s)
	}
	return NewKeymap(byte(min), int(r.KeysymsPerKeycode), syms), nil
}

// KeyCombo presses a key combination such as "ctrl+l" (xdotool names), repeat times.
func (c *Conn) KeyCombo(ctx context.Context, combo string, repeat int) error {
	cb, err := ParseCombo(combo)
	if err != nil {
		return err
	}
	km, err := c.keymap()
	if err != nil {
		return err
	}
	return newKeyboard(c, km).Press(ctx, cb, repeat)
}

// TypeText types text (any Unicode) into the window that has the focus.
func (c *Conn) TypeText(ctx context.Context, text string) error {
	km, err := c.keymap()
	if err != nil {
		return err
	}
	return newKeyboard(c, km).Type(ctx, text)
}
