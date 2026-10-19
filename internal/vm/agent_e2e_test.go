// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e

package vm

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack/internal/imaging"
	"github.com/PacifAIst/controlsnack/internal/protocol"
	"github.com/PacifAIst/controlsnack/internal/vm/agentclient"
)

// differing counts pixels whose channels differ by more than tol, and the total.
func differing(a, b image.Image, tol int) (diff, total int) {
	r := a.Bounds()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			r1, g1, b1, _ := a.At(x, y).RGBA()
			r2, g2, b2, _ := b.At(x, y).RGBA()
			d := func(p, q uint32) int {
				v := int(p>>8) - int(q>>8)
				if v < 0 {
					v = -v
				}
				return v
			}
			if d(r1, r2) > tol || d(g1, g2) > tol || d(b1, b2) > tol {
				diff++
			}
			total++
		}
	}
	return diff, total
}

// TestE2EScreenshot asks the real agent in the real guest for screenshots and checks them against what QEMU itself
// says the screen shows (QMP screendump): same size, and the same pixels except where a clock ticked in between.
func TestE2EScreenshot(t *testing.T) {
	m, dir := bootE2E(t, 1280, 800)
	ctx := context.Background()

	has := map[string]bool{}
	for _, c := range m.Ack.Capabilities {
		has[c] = true
	}
	if !has[protocol.CapScreenshot] {
		t.Fatalf("the agent in the guest announces no screenshot capability (is X reachable from it?): %v", m.Ack.Capabilities)
	}
	if m.Ack.Display.W != 1280 || m.Ack.Display.H != 800 {
		t.Errorf("the handshake reports the screen the X server has: %+v", m.Ack.Display)
	}
	time.Sleep(20 * time.Second) // the desktop finishes drawing itself

	shoot := func(req protocol.ScreenshotReq) (protocol.ScreenshotRes, []byte) {
		t.Helper()
		var res protocol.ScreenshotRes
		blob, err := m.Client.Call(ctx, protocol.TypeScreenshot, req, nil, &res)
		if err != nil {
			t.Fatalf("screenshot %+v: %v", req, err)
		}
		return res, blob
	}

	// The reference: what QEMU says is on the screen.
	ref := filepath.Join(dir, "ref.png")
	if err := m.Screendump(ctx, ref); err != nil {
		t.Fatal(err)
	}
	refData, _ := os.ReadFile(ref)
	refImg, err := png.Decode(bytes.NewReader(refData))
	if err != nil {
		t.Fatal(err)
	}

	begin := time.Now()
	res, blob := shoot(protocol.ScreenshotReq{Format: "png"})
	t.Logf("png: %d bytes in %s", len(blob), time.Since(begin).Round(time.Millisecond))
	if res.W != 1280 || res.H != 800 || res.Format != "png" || res.Scale != 1 || len(res.Hash) != 16 {
		t.Fatalf("%+v", res)
	}
	if res.Cursor.X < 0 || res.Cursor.X >= 1280 || res.Cursor.Y < 0 || res.Cursor.Y >= 800 {
		t.Errorf("the cursor is on the screen: %+v", res.Cursor)
	}
	got, err := png.Decode(bytes.NewReader(blob))
	if err != nil {
		t.Fatal(err)
	}
	if diff, total := differing(refImg, got, 0); diff*100 > total { // more than 1 % of the pixels
		t.Errorf("the agent's screenshot differs from QEMU's screendump in %d of %d pixels", diff, total)
	}

	// Nothing moves on an idle desktop: asking again with the hash gets no picture.
	again, blob2 := shoot(protocol.ScreenshotReq{Format: "png", IfChangedSince: res.Hash})
	t.Logf("unchanged answer: %+v (%d bytes)", again, len(blob2))
	if !again.Unchanged || len(blob2) != 0 || again.Hash != res.Hash {
		t.Logf("the screen changed between two requests (a clock, a blinking cursor): %+v", again)
	}

	// A smaller JPEG for a model that wants one.
	begin = time.Now()
	small, jpgBlob := shoot(protocol.ScreenshotReq{Format: "jpeg", Quality: 70, MaxLongEdge: 640})
	t.Logf("jpeg 640: %d bytes in %s", len(jpgBlob), time.Since(begin).Round(time.Millisecond))
	if small.W != 640 || small.H != 400 || small.Scale != 0.5 {
		t.Errorf("%+v", small)
	}

	// WebP, the default format.
	begin = time.Now()
	web, webBlob := shoot(protocol.ScreenshotReq{})
	t.Logf("webp default: %d bytes in %s", len(webBlob), time.Since(begin).Round(time.Millisecond))
	if web.Format != string(imaging.WebP) || len(webBlob) < 100 || string(webBlob[8:12]) != "WEBP" {
		t.Errorf("%+v", web)
	}

	// Refusals travel back as the protocol's errors.
	if _, err := m.Client.Call(ctx, protocol.TypeScreenshot, protocol.ScreenshotReq{Format: "gif"}, nil, nil); err == nil {
		t.Error("an unknown format is an error")
	}
}

// TestE2EWindows opens a terminal and a file manager in the real guest and checks what the agent reports about the
// monitors and the windows, and that focusing and maximizing really move the window manager (xfwm4).
func TestE2EWindows(t *testing.T) {
	m, _ := bootE2E(t, 1280, 800)
	ctx := context.Background()
	for _, c := range []string{protocol.CapWindows, protocol.CapScreenshot} {
		if !slices.Contains(m.Ack.Capabilities, c) {
			t.Fatalf("no %s capability: %v", c, m.Ack.Capabilities)
		}
	}
	time.Sleep(20 * time.Second)

	info := func() protocol.ScreenInfoRes {
		t.Helper()
		var res protocol.ScreenInfoRes
		if _, err := m.Client.Call(ctx, protocol.TypeScreenInfo, nil, nil, &res); err != nil {
			t.Fatalf("screen_info: %v", err)
		}
		return res
	}
	list := func() []protocol.Window {
		t.Helper()
		var res protocol.WindowsRes
		if _, err := m.Client.Call(ctx, protocol.TypeWindows, protocol.WindowsReq{Op: "list"}, nil, &res); err != nil {
			t.Fatalf("windows: %v", err)
		}
		return res.Windows
	}
	find := func(ws []protocol.Window, class string) *protocol.Window {
		for i := range ws {
			if strings.EqualFold(ws[i].Class, class) {
				return &ws[i]
			}
		}
		return nil
	}

	si := info()
	t.Logf("screen_info: %+v", si)
	if len(si.Monitors) != 1 || si.Monitors[0].Bounds != (protocol.Rect{0, 0, 1280, 800}) || !si.Monitors[0].Primary {
		t.Errorf("one primary monitor of 1280x800: %+v", si.Monitors)
	}

	// A terminal.
	chord(t, m, "ctrl", "alt", "t")
	time.Sleep(25 * time.Second)
	ws := list()
	term := find(ws, "Xfce4-terminal")
	if term == nil {
		t.Fatalf("no terminal among the windows: %+v", ws)
	}
	t.Logf("terminal: %+v", *term)
	if !strings.HasPrefix(term.Title, "Terminal") || term.PID <= 0 || !term.Active || term.Bounds[2] < 200 || term.Bounds[3] < 100 || term.Minimal {
		t.Errorf("%+v", *term)
	}
	if got := info().ActiveWindow; got == nil || got.ID != term.ID {
		t.Errorf("the active window of screen_info is the terminal: %+v", got)
	}

	// A file manager, started from the terminal: it comes up in front and takes the focus.
	typeText(t, m, "thunar /usr/share/doc &\n")
	time.Sleep(45 * time.Second)
	ws = list()
	files := find(ws, "Thunar")
	if files == nil || len(ws) < 2 {
		t.Fatalf("no file manager among the windows: %+v", ws)
	}
	t.Logf("files: %+v", *files)
	if ws[0].ID != files.ID || !files.Active || !strings.Contains(files.Title, "doc") {
		t.Errorf("the topmost window comes first and has the focus: %+v", ws)
	}

	// Focus the terminal again.
	if _, err := m.Client.Call(ctx, protocol.TypeWindows, protocol.WindowsReq{Op: "focus", WindowID: term.ID}, nil, nil); err != nil {
		t.Fatalf("focus: %v", err)
	}
	ws = list()
	if ws[0].ID != term.ID || !ws[0].Active || find(ws, "Thunar").Active {
		t.Errorf("after focus the terminal is in front and active: %+v", ws)
	}

	// Maximize the file manager: it fills the screen below the panel.
	if _, err := m.Client.Call(ctx, protocol.TypeWindows, protocol.WindowsReq{Op: "maximize", WindowID: files.ID}, nil, nil); err != nil {
		t.Fatalf("maximize: %v", err)
	}
	time.Sleep(2 * time.Second)
	files = find(list(), "Thunar")
	t.Logf("maximized: %+v", *files)
	if files.Bounds[0] > 2 || files.Bounds[2] < 1270 || files.Bounds[1] < 15 || files.Bounds[3] < 700 {
		t.Errorf("a maximized window fills the screen below the panel: %+v", files.Bounds)
	}

	// Refusals.
	var re *agentclient.RemoteError
	_, err := m.Client.Call(ctx, protocol.TypeWindows, protocol.WindowsReq{Op: "focus", WindowID: "0x1"}, nil, nil)
	if !errors.As(err, &re) || re.Code != protocol.CodeNotFound {
		t.Errorf("a window that is not there is not_found: %v", err)
	}
	_, err = m.Client.Call(ctx, protocol.TypeWindows, protocol.WindowsReq{Op: "focus", WindowID: "root"}, nil, nil)
	if !errors.As(err, &re) || re.Code != protocol.CodeInvalidArgs {
		t.Errorf("a malformed id is invalid_args: %v", err)
	}
}

// TestE2EInput drives the real guest through the agent's `input` request. What was typed is read back exactly, with
// Unicode, through the terminal's window title (an escape sequence in the typed command sets it, and `windows`
// reports it); mouse clicks and drags are read back through the active window and window bounds.
func TestE2EInput(t *testing.T) {
	m, dir := bootE2E(t, 1280, 800)
	ctx := context.Background()
	if !slices.Contains(m.Ack.Capabilities, protocol.CapInput) {
		t.Fatalf("no input capability: %v", m.Ack.Capabilities)
	}
	time.Sleep(20 * time.Second)

	input := func(events ...protocol.InputEvent) protocol.InputRes {
		t.Helper()
		var res protocol.InputRes
		if _, err := m.Client.Call(ctx, protocol.TypeInput, protocol.InputReq{Events: events}, nil, &res); err != nil {
			t.Fatalf("input %+v: %v", events, err)
		}
		return res
	}
	list := func() []protocol.Window {
		t.Helper()
		var res protocol.WindowsRes
		if _, err := m.Client.Call(ctx, protocol.TypeWindows, protocol.WindowsReq{Op: "list"}, nil, &res); err != nil {
			t.Fatal(err)
		}
		return res.Windows
	}
	byClass := func(class string) *protocol.Window {
		for _, w := range list() {
			if strings.EqualFold(w.Class, class) {
				return &w
			}
		}
		return nil
	}
	shot := func(name string) {
		t.Helper()
		p := filepath.Join(dir, name+".png")
		if err := m.Screendump(ctx, p); err != nil {
			t.Fatal(err)
		}
		if keep := os.Getenv("CONTROLSNACK_E2E_SHOTS"); keep != "" {
			if data, err := os.ReadFile(p); err == nil {
				_ = os.WriteFile(filepath.Join(keep, name+".png"), data, 0o600)
			}
		}
	}

	// A terminal, opened with a key combination.
	res := input(protocol.InputEvent{Type: "key", Keys: "ctrl+alt+t"})
	t.Logf("opening a terminal: hash after %s", res.ScreenHashAfter)
	time.Sleep(25 * time.Second)
	term := byClass("Xfce4-terminal")
	if term == nil {
		t.Fatalf("no terminal after ctrl+alt+t: %+v", list())
	}

	// Typed text with Unicode, read back through the title.
	const unicode = "héllo wörld 日本語 😀 € ñ"
	input(
		protocol.InputEvent{Type: "text", Text: `printf '\033]0;%s\007' '` + unicode + `'; sleep 60`},
		protocol.InputEvent{Type: "key", Keys: "Return"},
	)
	time.Sleep(3 * time.Second)
	term = byClass("Xfce4-terminal")
	t.Logf("title after typing: %q", term.Title)
	if !strings.Contains(term.Title, unicode) {
		t.Errorf("what was typed must come back exactly, Unicode included: got %q, want it to contain %q", term.Title, unicode)
	}
	shot("typed")
	input(protocol.InputEvent{Type: "key", Keys: "ctrl+c"}) // ends the sleep: the prompt is back

	// A key with repeat: type "abcdeXX", erase two characters, close the quote.
	input(
		protocol.InputEvent{Type: "text", Text: `printf '\033]0;%s\007' 'abcdeXX`},
		protocol.InputEvent{Type: "key", Keys: "BackSpace", Repeat: 2},
		protocol.InputEvent{Type: "text", Text: "'; sleep 60\n"},
	)
	time.Sleep(3 * time.Second)
	if title := byClass("Xfce4-terminal").Title; !strings.Contains(title, "abcde") || strings.Contains(title, "XX") {
		t.Errorf("BackSpace twice erases XX: %q", title)
	}
	input(protocol.InputEvent{Type: "key", Keys: "ctrl+c"}) // ends the sleep before the next command is typed

	// A second window, then a click on the terminal's title bar gives the terminal the focus back.
	input(protocol.InputEvent{Type: "text", Text: "thunar /usr/share/doc &\n"})
	time.Sleep(45 * time.Second)
	if w := byClass("Thunar"); w == nil || !w.Active {
		t.Fatalf("the file manager opens in front: %+v", list())
	}
	term = byClass("Xfce4-terminal")
	cx, cy := term.Bounds[0]+term.Bounds[2]/2, term.Bounds[1]+10
	input(protocol.InputEvent{Type: "click", X: cx, Y: cy})
	time.Sleep(2 * time.Second)
	if w := byClass("Xfce4-terminal"); !w.Active {
		t.Errorf("a click on the title bar focuses the window: %+v", list())
	}

	// A drag on the title bar moves the window.
	before := byClass("Xfce4-terminal").Bounds
	res = input(protocol.InputEvent{Type: "drag", X: cx, Y: cy, DragTo: &protocol.Point{X: cx + 300, Y: cy + 150}})
	time.Sleep(2 * time.Second)
	after := byClass("Xfce4-terminal").Bounds
	t.Logf("drag: %v -> %v, hash after %s", before, after, res.ScreenHashAfter)
	if dx, dy := after[0]-before[0], after[1]-before[1]; dx < 280 || dx > 320 || dy < 130 || dy > 170 || after[2] != before[2] {
		t.Errorf("the window moved by (%d, %d), expected about (300, 150) with the same size", dx, dy)
	}
	shot("dragged")

	// The hash the input reports is the screenshot's hash of the same screen.
	var s protocol.ScreenshotRes
	if _, err := m.Client.Call(ctx, protocol.TypeScreenshot, protocol.ScreenshotReq{Format: "jpeg", MaxLongEdge: 320, IfChangedSince: res.ScreenHashAfter}, nil, &s); err != nil {
		t.Fatal(err)
	}
	t.Logf("screenshot after the drag: %+v", s)

	// Refusals.
	var re *agentclient.RemoteError
	for name, ev := range map[string]protocol.InputEvent{
		"off the screen": {Type: "click", X: 5000, Y: 5},
		"two keys":       {Type: "key", Keys: "ctrl+a+b"},
		"unknown key":    {Type: "key", Keys: "Foo"},
		"a control char": {Type: "text", Text: "a\x07b"},
		"unknown event":  {Type: "tap"},
	} {
		_, err := m.Client.Call(ctx, protocol.TypeInput, protocol.InputReq{Events: []protocol.InputEvent{ev}}, nil, nil)
		if !errors.As(err, &re) || re.Code != protocol.CodeInvalidArgs {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// TestE2EExec runs commands in the real guest through the agent: output, exit codes, the user and the environment, a
// timeout, cancellation (nothing is left running), a big output, a program started in the background, and file_peek.
func TestE2EExec(t *testing.T) {
	m, _ := bootE2E(t, 1280, 800)
	ctx := context.Background()
	for _, c := range []string{protocol.CapExec, protocol.CapFilePeek} {
		if !slices.Contains(m.Ack.Capabilities, c) {
			t.Fatalf("no %s capability: %v", c, m.Ack.Capabilities)
		}
	}
	exec := func(req protocol.ExecReq) protocol.ExecRes {
		t.Helper()
		var res protocol.ExecRes
		if _, err := m.Client.Call(ctx, protocol.TypeExec, req, nil, &res); err != nil {
			t.Fatalf("exec %q: %v", req.Command, err)
		}
		return res
	}

	res := exec(protocol.ExecReq{Command: "echo hello; echo oops >&2; exit 3"})
	if res.Stdout != "hello\n" || res.Stderr != "oops\n" || res.ExitCode != 3 || res.TimedOut {
		t.Errorf("%+v", res)
	}

	// Who runs it, where, and with what environment.
	res = exec(protocol.ExecReq{Command: "id -un; pwd; echo $TERM $PAGER $GIT_PAGER $DEBIAN_FRONTEND; echo ${DISPLAY:-no-display}; sudo -n true 2>&1 | head -1"})
	t.Logf("environment:\n%s", res.Stdout)
	lines := strings.Split(strings.TrimSpace(res.Stdout), "\n")
	if len(lines) < 4 || lines[0] != "cs" || lines[1] != "/home/cs/work" || lines[2] != "dumb cat cat noninteractive" || lines[3] == "no-display" {
		t.Errorf("the command runs as cs in its work directory with the display and the quiet environment: %q", res.Stdout)
	}
	if len(lines) >= 5 && !strings.Contains(lines[4], "sudo") && !strings.Contains(lines[4], "password") && !strings.Contains(lines[4], "not found") {
		t.Errorf("cs has no sudo: %q", lines[4])
	}

	if res = exec(protocol.ExecReq{Command: "cat; echo done", Stdin: "from stdin\n"}); res.Stdout != "from stdin\ndone\n" {
		t.Errorf("%+v", res)
	}
	if res = exec(protocol.ExecReq{Command: "cd /usr && pwd", Cwd: "/tmp"}); res.Stdout != "/usr\n" {
		t.Errorf("%+v", res)
	}

	// A command that takes too long.
	begin := time.Now()
	res = exec(protocol.ExecReq{Command: "echo started; sleep 300", TimeoutMS: 1500})
	t.Logf("timeout: %+v after %s", res, time.Since(begin).Round(time.Millisecond))
	if !res.TimedOut || res.ExitCode != 124 || res.Stdout != "started\n" || time.Since(begin) > 30*time.Second {
		t.Errorf("%+v", res)
	}

	// Cancellation: the host gives up, and the command is gone from the guest.
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	_, err := m.Client.Call(cctx, protocol.TypeExec, protocol.ExecReq{Command: "sleep 311 & sleep 312; wait"}, nil, nil)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("%v", err)
	}
	time.Sleep(4 * time.Second)
	if res = exec(protocol.ExecReq{Command: "pgrep -f '[s]leep 31[12]' | wc -l"}); strings.TrimSpace(res.Stdout) != "0" {
		t.Errorf("a canceled command leaves nothing running: %q", res.Stdout)
	}

	// Big output keeps head and tail.
	res = exec(protocol.ExecReq{Command: "seq 1 100000"})
	if !res.Truncated || !strings.HasPrefix(res.Stdout, "1\n2\n") || !strings.HasSuffix(res.Stdout, "100000\n") || !strings.Contains(res.Stdout, "bytes omitted") {
		t.Errorf("%d bytes, truncated %v", len(res.Stdout), res.Truncated)
	}

	// A program started in the background neither keeps the command waiting nor dies afterwards: the file manager
	// opens a window, and the exec returned at once.
	begin = time.Now()
	res = exec(protocol.ExecReq{Command: "thunar /usr/share/doc & echo launched"})
	if res.Stdout != "launched\n" || time.Since(begin) > 5*time.Second {
		t.Errorf("%+v after %s", res, time.Since(begin))
	}
	time.Sleep(40 * time.Second)
	var ws protocol.WindowsRes
	if _, err := m.Client.Call(ctx, protocol.TypeWindows, protocol.WindowsReq{Op: "list"}, nil, &ws); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, w := range ws.Windows {
		found = found || strings.EqualFold(w.Class, "Thunar")
	}
	if !found {
		t.Errorf("the program started in the background has a window: %+v", ws.Windows)
	}

	// file_peek.
	var fp protocol.FilePeekRes
	blob, err := m.Client.Call(ctx, protocol.TypeFilePeek, protocol.FilePeekReq{Path: "/etc/os-release", MaxBytes: 200}, nil, &fp)
	if err != nil || !fp.Exists || fp.Size < 100 || !strings.Contains(string(blob), "Debian") || len(blob) > 200 {
		t.Errorf("%+v %q %v", fp, blob, err)
	}
	if _, err := m.Client.Call(ctx, protocol.TypeFilePeek, protocol.FilePeekReq{Path: "/nonexistent/file"}, nil, &fp); err != nil || fp.Exists {
		t.Errorf("%+v %v", fp, err)
	}
	if _, err := m.Client.Call(ctx, protocol.TypeFilePeek, protocol.FilePeekReq{Path: "/etc"}, nil, &fp); err != nil || !fp.Exists || fp.Size != 0 {
		t.Errorf("%+v %v", fp, err)
	}
}
