// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/PacifAIst/controlsnack/internal/agent/x11"
	"github.com/PacifAIst/controlsnack/internal/protocol"
)

type fakeDesktop struct {
	windows    []x11.WindowInfo
	active     *x11.WindowInfo
	monitors   []x11.MonitorInfo
	px, py     int
	ptrErr     error
	winErr     error
	monErr     error
	actErr     error
	focused    []uint32
	maximized  []uint32
	lastCtxErr error
}

func (f *fakeDesktop) Windows(ctx context.Context) ([]x11.WindowInfo, error) {
	f.lastCtxErr = ctx.Err()
	return f.windows, f.winErr
}
func (f *fakeDesktop) ActiveWindow(context.Context) (*x11.WindowInfo, error) { return f.active, nil }
func (f *fakeDesktop) Activate(_ context.Context, id uint32) error {
	f.focused = append(f.focused, id)
	return f.actErr
}
func (f *fakeDesktop) Maximize(_ context.Context, id uint32) error {
	f.maximized = append(f.maximized, id)
	return f.actErr
}
func (f *fakeDesktop) Monitors(context.Context) ([]x11.MonitorInfo, error) {
	return f.monitors, f.monErr
}
func (f *fakeDesktop) Pointer() (int, int, error) { return f.px, f.py, f.ptrErr }

func twoWindows() *fakeDesktop {
	term := x11.WindowInfo{ID: 0x3400007, Title: "Terminal - cs@controlsnack: ~", Class: "Xfce4-terminal", PID: 904, X: 0, Y: 28, W: 826, H: 518, Active: true}
	files := x11.WindowInfo{ID: 0x3600003, Title: "doc - Thunar", Class: "Thunar", PID: 1212, X: 635, Y: 296, W: 650, H: 514, Minimized: true}
	return &fakeDesktop{
		windows:  []x11.WindowInfo{term, files},
		active:   &term,
		monitors: []x11.MonitorInfo{{ID: 0, W: 1280, H: 800, Primary: true}},
		px:       640, py: 400,
	}
}

func frame(t *testing.T, typ string, body any) protocol.Frame {
	t.Helper()
	f, err := protocol.NewFrame(1, typ, body, nil)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestScreenInfo(t *testing.T) {
	d := twoWindows()
	d.monitors = append(d.monitors, x11.MonitorInfo{ID: 1, X: 1280, W: 1920, H: 1080})
	res, blob, err := DesktopHandlers{d}.ScreenInfo(context.Background(), frame(t, protocol.TypeScreenInfo, nil))
	if err != nil || blob != nil {
		t.Fatalf("%v %v", err, blob)
	}
	got := res.(protocol.ScreenInfoRes)
	if len(got.Monitors) != 2 || got.Monitors[0] != (protocol.Monitor{ID: 0, Bounds: protocol.Rect{0, 0, 1280, 800}, Primary: true}) ||
		got.Monitors[1].Bounds != (protocol.Rect{1280, 0, 1920, 1080}) || got.Monitors[1].Primary {
		t.Errorf("%+v", got.Monitors)
	}
	if got.Cursor != (protocol.Point{X: 640, Y: 400}) {
		t.Errorf("%+v", got.Cursor)
	}
	if got.ActiveWindow == nil || got.ActiveWindow.ID != "0x3400007" || got.ActiveWindow.Title != "Terminal - cs@controlsnack: ~" || !got.ActiveWindow.Active {
		t.Errorf("%+v", got.ActiveWindow)
	}
}

func TestScreenInfoOnADesktopWithoutAWindowManager(t *testing.T) {
	d := &fakeDesktop{monitors: []x11.MonitorInfo{{W: 1024, H: 768, Primary: true}}, ptrErr: errors.New("no pointer")}
	res, _, err := DesktopHandlers{d}.ScreenInfo(context.Background(), frame(t, protocol.TypeScreenInfo, nil))
	if err != nil {
		t.Fatal(err)
	}
	got := res.(protocol.ScreenInfoRes)
	if got.ActiveWindow != nil || got.Cursor != (protocol.Point{}) || len(got.Monitors) != 1 {
		t.Errorf("no focused window and no pointer are facts, not failures: %+v", got)
	}
}

func TestScreenInfoErrors(t *testing.T) {
	d := twoWindows()
	d.monErr = errors.New("BadRequest")
	if _, _, err := (DesktopHandlers{d}).ScreenInfo(context.Background(), frame(t, protocol.TypeScreenInfo, nil)); code(err) != protocol.CodeInternal || !strings.Contains(err.Error(), "BadRequest") {
		t.Errorf("%v", err)
	}
	if _, _, err := (DesktopHandlers{twoWindows()}).ScreenInfo(context.Background(), frame(t, protocol.TypeScreenInfo, map[string]any{"verbose": true})); code(err) != protocol.CodeInvalidArgs {
		t.Errorf("a request takes no body: %v", err)
	}
}

func TestWindowsList(t *testing.T) {
	for _, req := range []protocol.WindowsReq{{Op: "list"}, {}} { // an empty op means list
		res, _, err := DesktopHandlers{twoWindows()}.Windows(context.Background(), frame(t, protocol.TypeWindows, req))
		if err != nil {
			t.Fatal(err)
		}
		ws := res.(protocol.WindowsRes).Windows
		if len(ws) != 2 {
			t.Fatalf("%+v", ws)
		}
		if ws[0] != (protocol.Window{ID: "0x3400007", Title: "Terminal - cs@controlsnack: ~", Class: "Xfce4-terminal", PID: 904,
			Bounds: protocol.Rect{0, 28, 826, 518}, Active: true}) {
			t.Errorf("%+v", ws[0])
		}
		if ws[1].ID != "0x3600003" || !ws[1].Minimal || ws[1].Active || ws[1].Class != "Thunar" {
			t.Errorf("%+v", ws[1])
		}
	}
	empty := &fakeDesktop{}
	res, _, err := DesktopHandlers{empty}.Windows(context.Background(), frame(t, protocol.TypeWindows, protocol.WindowsReq{Op: "list"}))
	if err != nil || len(res.(protocol.WindowsRes).Windows) != 0 {
		t.Errorf("no windows is an empty list: %v %+v", err, res)
	}
}

func TestWindowsFocusAndMaximize(t *testing.T) {
	d := twoWindows()
	h := DesktopHandlers{d}
	for _, op := range []string{"focus", "maximize"} {
		res, blob, err := h.Windows(context.Background(), frame(t, protocol.TypeWindows, protocol.WindowsReq{Op: op, WindowID: "0x3600003"}))
		if err != nil || blob != nil || len(res.(protocol.WindowsRes).Windows) != 0 {
			t.Errorf("%s answers with an acknowledgement: %v %+v", op, err, res)
		}
	}
	if len(d.focused) != 1 || d.focused[0] != 0x3600003 || len(d.maximized) != 1 || d.maximized[0] != 0x3600003 {
		t.Errorf("the id reaches the display as a number: focus %v maximize %v", d.focused, d.maximized)
	}
}

func TestWindowsRefusals(t *testing.T) {
	tests := []struct {
		name string
		req  any
		want string
		code string
	}{
		{"unknown op", protocol.WindowsReq{Op: "close", WindowID: "0x3600003"}, "not list, focus or maximize", protocol.CodeInvalidArgs},
		{"focus without an id", protocol.WindowsReq{Op: "focus"}, "needs a window_id", protocol.CodeInvalidArgs},
		{"maximize without an id", protocol.WindowsReq{Op: "maximize"}, "needs a window_id", protocol.CodeInvalidArgs},
		{"list with an id", protocol.WindowsReq{Op: "list", WindowID: "0x1"}, "no window_id", protocol.CodeInvalidArgs},
		{"an id that is not one", protocol.WindowsReq{Op: "focus", WindowID: "root"}, "window_id", protocol.CodeInvalidArgs},
		{"decimal id", protocol.WindowsReq{Op: "focus", WindowID: "54525959"}, "window_id", protocol.CodeInvalidArgs},
		{"unknown field", map[string]any{"op": "list", "force": true}, "unknown field", protocol.CodeInvalidArgs},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := twoWindows()
			_, _, err := DesktopHandlers{d}.Windows(context.Background(), frame(t, protocol.TypeWindows, tt.req))
			if code(err) != tt.code || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("%v", err)
			}
			if len(d.focused)+len(d.maximized) != 0 {
				t.Error("a refused request must not reach the display")
			}
		})
	}
}

func TestWindowsErrorsFromTheDisplay(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code string
	}{
		{"closed window", fmt.Errorf("%w: 0x1", x11.ErrNoSuchWindow), protocol.CodeNotFound},
		{"window manager too slow", x11.ErrNotApplied, protocol.CodeTimeout},
		{"anything else", errors.New("BadWindow"), protocol.CodeInternal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := twoWindows()
			d.actErr = tt.err
			_, _, err := DesktopHandlers{d}.Windows(context.Background(), frame(t, protocol.TypeWindows, protocol.WindowsReq{Op: "focus", WindowID: "0x3600003"}))
			if code(err) != tt.code {
				t.Errorf("%v", err)
			}
		})
	}
	d := twoWindows()
	d.winErr = errors.New("BadAccess")
	if _, _, err := (DesktopHandlers{d}).Windows(context.Background(), frame(t, protocol.TypeWindows, protocol.WindowsReq{Op: "list"})); code(err) != protocol.CodeInternal {
		t.Errorf("%v", err)
	}
}

func TestACanceledRequestStaysACancellation(t *testing.T) {
	d := twoWindows()
	d.actErr = x11.ErrNotApplied
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := DesktopHandlers{d}.Windows(ctx, frame(t, protocol.TypeWindows, protocol.WindowsReq{Op: "focus", WindowID: "0x3600003"}))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("the dispatcher answers a canceled request itself: %v", err)
	}
}
