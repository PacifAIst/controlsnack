// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package agent

import (
	"context"
	"errors"

	"github.com/PacifAIst/controlsnack-local/internal/agent/server"
	"github.com/PacifAIst/controlsnack-local/internal/agent/x11"
	"github.com/PacifAIst/controlsnack-local/internal/protocol"
)

// Desktop is what the window and monitor handlers need from the display; *x11.Conn is the real one.
type Desktop interface {
	Windows(ctx context.Context) ([]x11.WindowInfo, error)
	ActiveWindow(ctx context.Context) (*x11.WindowInfo, error)
	Activate(ctx context.Context, id uint32) error
	Maximize(ctx context.Context, id uint32) error
	Monitors(ctx context.Context) ([]x11.MonitorInfo, error)
	Pointer() (x, y int, err error)
}

// DesktopHandlers serve `screen_info` and `windows` (PLAN.md section 5.1).
type DesktopHandlers struct{ Desktop Desktop }

// Window operations of the `windows` request.
const (
	opList     = "list"
	opFocus    = "focus"
	opMaximize = "maximize"
)

// ScreenInfo answers `screen_info`: the monitors, where the pointer is and which window has the focus. A desktop
// without a window manager has no active window, which is not an error.
func (d DesktopHandlers) ScreenInfo(ctx context.Context, req protocol.Frame) (any, []byte, error) {
	var body struct{}
	if err := protocol.DecodeBody(req, &body); err != nil {
		return nil, nil, server.Fail(protocol.CodeInvalidArgs, "%v", err)
	}
	mons, err := d.Desktop.Monitors(ctx)
	if err != nil {
		return nil, nil, mapError(ctx, err, "reading the monitors")
	}
	res := protocol.ScreenInfoRes{Monitors: make([]protocol.Monitor, 0, len(mons))}
	for _, m := range mons {
		res.Monitors = append(res.Monitors, protocol.Monitor{ID: m.ID, Bounds: protocol.Rect{m.X, m.Y, m.W, m.H}, Primary: m.Primary})
	}
	if x, y, err := d.Desktop.Pointer(); err == nil {
		res.Cursor = protocol.Point{X: x, Y: y}
	}
	if w, err := d.Desktop.ActiveWindow(ctx); err == nil && w != nil {
		pw := toWindow(*w)
		res.ActiveWindow = &pw
	}
	return res, nil, nil
}

// Windows answers `windows`: op list (the default), focus or maximize.
func (d DesktopHandlers) Windows(ctx context.Context, req protocol.Frame) (any, []byte, error) {
	var r protocol.WindowsReq
	if err := protocol.DecodeBody(req, &r); err != nil {
		return nil, nil, server.Fail(protocol.CodeInvalidArgs, "%v", err)
	}
	op := r.Op
	if op == "" {
		op = opList
	}
	switch op {
	case opList:
		if r.WindowID != "" {
			return nil, nil, server.Fail(protocol.CodeInvalidArgs, "list takes no window_id")
		}
		ws, err := d.Desktop.Windows(ctx)
		if err != nil {
			return nil, nil, mapError(ctx, err, "listing the windows")
		}
		res := protocol.WindowsRes{Windows: make([]protocol.Window, 0, len(ws))}
		for _, w := range ws {
			res.Windows = append(res.Windows, toWindow(w))
		}
		return res, nil, nil
	case opFocus, opMaximize:
		if r.WindowID == "" {
			return nil, nil, server.Fail(protocol.CodeInvalidArgs, "%s needs a window_id from the list", op)
		}
		id, err := x11.ParseWindowID(r.WindowID)
		if err != nil {
			return nil, nil, server.Fail(protocol.CodeInvalidArgs, "window_id: %v", err)
		}
		if op == opFocus {
			err = d.Desktop.Activate(ctx, id)
		} else {
			err = d.Desktop.Maximize(ctx, id)
		}
		if err != nil {
			return nil, nil, mapError(ctx, err, op+" failed")
		}
		return protocol.WindowsRes{}, nil, nil
	}
	return nil, nil, server.Fail(protocol.CodeInvalidArgs, "op %q is not list, focus or maximize", r.Op)
}

func toWindow(w x11.WindowInfo) protocol.Window {
	return protocol.Window{ID: x11.FormatWindowID(w.ID), Title: w.Title, Class: w.Class, PID: w.PID,
		Bounds: protocol.Rect{w.X, w.Y, w.W, w.H}, Active: w.Active, Minimal: w.Minimized}
}

// mapError turns what the display said into the protocol's error codes. A request the host canceled stays a
// cancellation (the dispatcher answers it), whatever was in progress.
func mapError(ctx context.Context, err error, what string) error {
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(err, x11.ErrNoSuchWindow):
		return server.Fail(protocol.CodeNotFound, "%v", err)
	case errors.Is(err, x11.ErrNotApplied):
		return server.Fail(protocol.CodeTimeout, "%v", err)
	}
	return server.Fail(protocol.CodeInternal, "%s: %v", what, err)
}
