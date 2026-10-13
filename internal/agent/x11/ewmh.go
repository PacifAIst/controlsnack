// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package x11

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jezek/xgb/xproto"
)

// WindowInfo is a top-level window as the window manager reports it (EWMH). Bounds are the outer rectangle in screen
// pixels, the frame included.
type WindowInfo struct {
	ID                uint32
	Title, Class      string
	PID               int
	X, Y, W, H        int
	Active, Minimized bool
	MaximizedBothWays bool
	// Shell marks windows of the desktop itself (panels, the desktop background, menus): they are in the window
	// manager's list but are not windows a task is done in, so they are left out of listings and cannot be focused.
	Shell bool
}

// ErrNoSuchWindow means the id is not one of the window manager's client windows (it was closed, or never was one).
var ErrNoSuchWindow = errors.New("no such window")

// ErrNotApplied means the window manager did not do what was asked within the time allowed.
var ErrNotApplied = errors.New("the window manager did not apply the request in time")

// maxWindows bounds what one listing reads: every window costs a handful of round trips.
const maxWindows = 100

// applyTimeout is how long Activate and Maximize wait for the window manager to show the effect.
const applyTimeout = 1500 * time.Millisecond

// atom returns the atom of a name, interning it on first use.
func (c *Conn) atom(name string) (xproto.Atom, error) {
	c.atomMu.Lock()
	if a, ok := c.atoms[name]; ok {
		c.atomMu.Unlock()
		return a, nil
	}
	c.atomMu.Unlock()
	r, err := xproto.InternAtom(c.c, false, uint16(len(name)), name).Reply() //nolint:gosec // our own short constant names
	if err != nil {
		return 0, fmt.Errorf("interning %s: %w", name, err)
	}
	c.atomMu.Lock()
	c.atoms[name] = r.Atom
	c.atomMu.Unlock()
	return r.Atom, nil
}

// property reads a window property and returns its raw value (nil when the window has no such property).
func (c *Conn) property(win xproto.Window, prop xproto.Atom, typ xproto.Atom, longs uint32) ([]byte, error) {
	r, err := xproto.GetProperty(c.c, false, win, prop, typ, 0, longs).Reply()
	if err != nil {
		return nil, err
	}
	if r.ValueLen == 0 {
		return nil, nil
	}
	return r.Value, nil
}

// clientList returns the window manager's windows, bottom to top. Without a window manager (a bare Xvfb) the property
// does not exist and the list is empty.
func (c *Conn) clientList() ([]uint32, error) {
	for _, name := range []string{"_NET_CLIENT_LIST_STACKING", "_NET_CLIENT_LIST"} {
		a, err := c.atom(name)
		if err != nil {
			return nil, err
		}
		v, err := c.property(c.root, a, xproto.AtomWindow, 4096)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", name, err)
		}
		if ids := cardinals(v); len(ids) > 0 {
			return ids, nil
		}
	}
	return nil, nil
}

// activeWindow is the focused client window, or 0.
func (c *Conn) activeWindow() (uint32, error) {
	a, err := c.atom("_NET_ACTIVE_WINDOW")
	if err != nil {
		return 0, err
	}
	v, err := c.property(c.root, a, xproto.AtomWindow, 1)
	if err != nil {
		return 0, fmt.Errorf("reading the active window: %w", err)
	}
	if ids := cardinals(v); len(ids) > 0 {
		return ids[0], nil
	}
	return 0, nil
}

// Windows lists the window manager's client windows, topmost first.
func (c *Conn) Windows(ctx context.Context) ([]WindowInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ids, err := c.clientList()
	if err != nil {
		return nil, err
	}
	active, err := c.activeWindow()
	if err != nil {
		return nil, err
	}
	// Bottom to top in EWMH; the model wants the window in front first.
	slices.Reverse(ids)
	if len(ids) > maxWindows {
		ids = ids[:maxWindows]
	}
	var out []WindowInfo
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		w, ok := c.windowInfo(id)
		if !ok || w.Shell {
			continue // closed while we were reading, or part of the desktop itself
		}
		w.Active = id == active
		out = append(out, w)
	}
	return out, nil
}

// windowInfo reads everything about one window with its requests in flight together.
func (c *Conn) windowInfo(id uint32) (WindowInfo, bool) {
	win := xproto.Window(id)
	names := []string{"_NET_WM_NAME", "UTF8_STRING", "_NET_WM_PID", "_NET_FRAME_EXTENTS", "_NET_WM_STATE", "_NET_WM_STATE_HIDDEN", "_NET_WM_WINDOW_TYPE",
		"_NET_WM_STATE_MAXIMIZED_VERT", "_NET_WM_STATE_MAXIMIZED_HORZ"}
	at := make(map[string]xproto.Atom, len(names))
	for _, n := range names {
		a, err := c.atom(n)
		if err != nil {
			return WindowInfo{}, false
		}
		at[n] = a
	}
	title := xproto.GetProperty(c.c, false, win, at["_NET_WM_NAME"], at["UTF8_STRING"], 0, 256)
	oldTitle := xproto.GetProperty(c.c, false, win, xproto.AtomWmName, xproto.AtomAny, 0, 256)
	class := xproto.GetProperty(c.c, false, win, xproto.AtomWmClass, xproto.AtomString, 0, 64)
	pid := xproto.GetProperty(c.c, false, win, at["_NET_WM_PID"], xproto.AtomCardinal, 0, 1)
	extents := xproto.GetProperty(c.c, false, win, at["_NET_FRAME_EXTENTS"], xproto.AtomCardinal, 0, 4)
	state := xproto.GetProperty(c.c, false, win, at["_NET_WM_STATE"], xproto.AtomAtom, 0, 64)
	wtype := xproto.GetProperty(c.c, false, win, at["_NET_WM_WINDOW_TYPE"], xproto.AtomAtom, 0, 16)
	geom := xproto.GetGeometry(c.c, xproto.Drawable(win))
	origin := xproto.TranslateCoordinates(c.c, win, c.root, 0, 0)

	var w WindowInfo
	w.ID = id
	g, err := geom.Reply()
	o, err2 := origin.Reply()
	if err != nil || err2 != nil {
		drain(title, oldTitle, class, pid, extents, state, wtype)
		return WindowInfo{}, false
	}
	value := func(ck xproto.GetPropertyCookie) []byte {
		r, err := ck.Reply()
		if err != nil || r.ValueLen == 0 {
			return nil
		}
		return r.Value
	}
	w.Title = textProperty(value(title), maxTitleRunes)
	if t := textProperty(value(oldTitle), maxTitleRunes); w.Title == "" {
		w.Title = t
	}
	w.Class = parseWMClass(value(class))
	if p := cardinals(value(pid)); len(p) > 0 {
		w.PID = int(p[0])
	}
	var left, right, top, bottom uint32
	if e := cardinals(value(extents)); len(e) == 4 {
		left, right, top, bottom = e[0], e[1], e[2], e[3]
	}
	b := frameBounds(int(o.DstX), int(o.DstY), int(g.Width), int(g.Height), left, right, top, bottom)
	w.X, w.Y, w.W, w.H = b[0], b[1], b[2], b[3]
	states := cardinals(value(state))
	w.Minimized = slices.Contains(states, uint32(at["_NET_WM_STATE_HIDDEN"]))
	w.Shell = c.isShellType(cardinals(value(wtype)))
	w.MaximizedBothWays = slices.Contains(states, uint32(at["_NET_WM_STATE_MAXIMIZED_VERT"])) && slices.Contains(states, uint32(at["_NET_WM_STATE_MAXIMIZED_HORZ"]))
	return w, true
}

// drain collects replies that will not be used, so the connection does not hold on to them.
func drain(cookies ...xproto.GetPropertyCookie) {
	for _, ck := range cookies {
		_, _ = ck.Reply()
	}
}

// requireWindow checks that id is one of the window manager's client windows.
func (c *Conn) requireWindow(id uint32) error {
	ids, err := c.clientList()
	if err != nil {
		return err
	}
	if !slices.Contains(ids, id) {
		return fmt.Errorf("%w: %s", ErrNoSuchWindow, FormatWindowID(id))
	}
	// The panel and the desktop are in the list too; they are not windows to work in.
	if w, ok := c.windowInfo(id); !ok || w.Shell {
		return fmt.Errorf("%w: %s", ErrNoSuchWindow, FormatWindowID(id))
	}
	return nil
}

// sendToRoot delivers a ClientMessage to the window manager the way EWMH wants it: to the root window, with the
// substructure masks, so the manager sees it as a request.
func (c *Conn) sendToRoot(window uint32, typ xproto.Atom, data [5]uint32) error {
	mask := uint32(xproto.EventMaskSubstructureNotify | xproto.EventMaskSubstructureRedirect)
	if err := xproto.SendEventChecked(c.c, false, c.root, mask, clientMessage(window, typ, data)).Check(); err != nil {
		return fmt.Errorf("asking the window manager: %w", err)
	}
	return nil
}

// source indication of EWMH: 2 says "a pager or other tool", which window managers honor even for focus requests.
const sourceTool = 2

// Activate gives the window the focus and raises it (un-minimizing it), then waits for the window manager to do so.
func (c *Conn) Activate(ctx context.Context, id uint32) error {
	if err := c.requireWindow(id); err != nil {
		return err
	}
	a, err := c.atom("_NET_ACTIVE_WINDOW")
	if err != nil {
		return err
	}
	if err := c.sendToRoot(id, a, [5]uint32{sourceTool, 0, 0, 0, 0}); err != nil {
		return err
	}
	return c.waitFor(ctx, func() (bool, error) {
		cur, err := c.activeWindow()
		return cur == id, err
	})
}

// Maximize maximizes the window in both directions and waits for the window manager to do so.
func (c *Conn) Maximize(ctx context.Context, id uint32) error {
	if err := c.requireWindow(id); err != nil {
		return err
	}
	state, err := c.atom("_NET_WM_STATE")
	if err != nil {
		return err
	}
	vert, err := c.atom("_NET_WM_STATE_MAXIMIZED_VERT")
	if err != nil {
		return err
	}
	horz, err := c.atom("_NET_WM_STATE_MAXIMIZED_HORZ")
	if err != nil {
		return err
	}
	const stateAdd = 1
	if err := c.sendToRoot(id, state, [5]uint32{stateAdd, uint32(vert), uint32(horz), sourceTool, 0}); err != nil {
		return err
	}
	return c.waitFor(ctx, func() (bool, error) {
		w, ok := c.windowInfo(id)
		return ok && w.MaximizedBothWays, nil
	})
}

// waitFor polls until the condition holds, the time allowed passes (ErrNotApplied) or the context ends.
func (c *Conn) waitFor(ctx context.Context, cond func() (bool, error)) error {
	deadline := time.Now().Add(applyTimeout)
	for {
		ok, err := cond()
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		if time.Now().After(deadline) {
			return ErrNotApplied
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

// ActiveWindow is the focused window, or nil when no client window has the focus (or there is no window manager).
func (c *Conn) ActiveWindow(ctx context.Context) (*WindowInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id, err := c.activeWindow()
	if err != nil || id == 0 {
		return nil, err
	}
	w, ok := c.windowInfo(id)
	if !ok || w.Shell {
		return nil, nil
	}
	w.Active = true
	return &w, nil
}

// shellTypes are the EWMH window types of the desktop's own furniture. A window is a shell window when it declares
// types and all of them are among these; a window without a type is a normal one.
var shellTypes = []string{
	"_NET_WM_WINDOW_TYPE_DESKTOP", "_NET_WM_WINDOW_TYPE_DOCK", "_NET_WM_WINDOW_TYPE_TOOLTIP", "_NET_WM_WINDOW_TYPE_DROPDOWN_MENU",
	"_NET_WM_WINDOW_TYPE_POPUP_MENU", "_NET_WM_WINDOW_TYPE_COMBO", "_NET_WM_WINDOW_TYPE_DND", "_NET_WM_WINDOW_TYPE_NOTIFICATION",
}

func (c *Conn) isShellType(types []uint32) bool {
	if len(types) == 0 {
		return false
	}
	shell := map[uint32]bool{}
	for _, name := range shellTypes {
		if a, err := c.atom(name); err == nil {
			shell[uint32(a)] = true
		}
	}
	return allIn(types, shell)
}

// allIn reports whether every value of a non-empty list is in the set.
func allIn(values []uint32, set map[uint32]bool) bool {
	if len(values) == 0 {
		return false
	}
	for _, v := range values {
		if !set[v] {
			return false
		}
	}
	return true
}
