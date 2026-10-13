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

	"github.com/PacifAIst/controlsnack-local/internal/imaging"
	"github.com/PacifAIst/controlsnack-local/internal/protocol"
	"github.com/PacifAIst/controlsnack-local/internal/vm/agentclient"
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
