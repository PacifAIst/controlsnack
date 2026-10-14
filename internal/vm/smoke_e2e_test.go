// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e

package vm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/protocol"
)

// TestE2EImageSmoke is the check of the finished image (PLAN.md P4-T13): it boots it and uses the desktop the way the
// model will: the screen has the size that was asked for, commands run as the unprivileged user, a terminal does not
// keep the screen from going quiet, text typed into an editor comes back through the accessibility tree and the
// recognizer, a web page on disk opens, and a file manager scrolls, opens a file on a double click and shows a menu on
// a right click. Set CS_E2E_SHOTS to a directory to keep the screenshots it takes.
func TestE2EImageSmoke(t *testing.T) {
	m, _ := bootE2E(t, 1280, 800)
	ctx := context.Background()
	t.Logf("boot: desktop up after %s, agent connected after %s", m.Timings.PortOpen.Round(time.Second), m.Timings.Connected.Round(time.Second))
	time.Sleep(20 * time.Second)

	call := func(typ string, req, res any) {
		t.Helper()
		if _, err := m.Client.Call(ctx, typ, req, nil, res); err != nil {
			t.Fatalf("%s %+v: %v", typ, req, err)
		}
	}
	exec := func(cmd string) protocol.ExecRes {
		t.Helper()
		var res protocol.ExecRes
		call(protocol.TypeExec, protocol.ExecReq{Command: cmd, TimeoutMS: 120000, MaxOutput: 32 << 10}, &res)
		return res
	}
	input := func(events ...protocol.InputEvent) {
		t.Helper()
		call(protocol.TypeInput, protocol.InputReq{Events: events}, nil)
	}
	windows := func() []protocol.Window {
		t.Helper()
		var res protocol.WindowsRes
		call(protocol.TypeWindows, protocol.WindowsReq{Op: "list"}, &res)
		return res.Windows
	}
	byClass := func(class string) *protocol.Window {
		for _, w := range windows() {
			if strings.EqualFold(w.Class, class) {
				return &w
			}
		}
		return nil
	}
	waitWindow := func(class string, within time.Duration) *protocol.Window {
		t.Helper()
		deadline := time.Now().Add(within)
		for time.Now().Before(deadline) {
			if w := byClass(class); w != nil {
				return w
			}
			time.Sleep(3 * time.Second)
		}
		t.Fatalf("no %s window within %s: %+v", class, within, windows())
		return nil
	}
	focus := func(w *protocol.Window) {
		t.Helper()
		call(protocol.TypeWindows, protocol.WindowsReq{Op: "focus", WindowID: w.ID}, nil)
		time.Sleep(2 * time.Second)
	}
	tree := func(req protocol.A11yTreeReq) protocol.A11yTreeRes {
		t.Helper()
		req.BudgetMS = 20000 // tcg is several times slower than hardware
		var res protocol.A11yTreeRes
		call(protocol.TypeA11yTree, req, &res)
		return res
	}
	names := func(res protocol.A11yTreeRes) map[string]bool {
		out := map[string]bool{}
		for _, n := range res.Nodes {
			if n.Name != "" {
				out[n.Name] = true
			}
		}
		return out
	}
	idle := func(what string, limit time.Duration) {
		t.Helper()
		var res protocol.WaitForRes
		call(protocol.TypeWaitFor, protocol.WaitForReq{Condition: "screen_idle", TimeoutMS: int(limit.Milliseconds())}, &res)
		t.Logf("screen_idle %s: met=%v after %d ms", what, res.Met, res.WaitedMS)
		if !res.Met {
			t.Errorf("the screen did not go quiet with %s in front (a blinking cursor, an animation)", what)
		}
	}
	shot := func(name string) {
		t.Helper()
		dir := os.Getenv("CS_E2E_SHOTS")
		if dir == "" {
			return
		}
		var res protocol.ScreenshotRes
		blob, err := m.Client.Call(ctx, protocol.TypeScreenshot, protocol.ScreenshotReq{Format: "png"}, nil, &res)
		if err != nil {
			t.Errorf("screenshot %s: %v", name, err)
			return
		}
		_ = os.WriteFile(filepath.Join(dir, name+".png"), blob, 0o600)
	}
	center := func(b protocol.Rect) (int, int) { return b[0] + b[2]/2, b[1] + b[3]/2 }

	// The screen is the size that was asked for, whichever way it is asked.
	var info protocol.ScreenInfoRes
	call(protocol.TypeScreenInfo, struct{}{}, &info)
	if len(info.Monitors) != 1 || info.Monitors[0].Bounds != (protocol.Rect{0, 0, 1280, 800}) {
		t.Errorf("screen_info: %+v", info.Monitors)
	}
	var snap protocol.ScreenshotRes
	call(protocol.TypeScreenshot, protocol.ScreenshotReq{Format: "png"}, &snap)
	if snap.W != 1280 || snap.H != 800 {
		t.Errorf("the screenshot is %dx%d", snap.W, snap.H)
	}
	shot("00-desktop")

	// Commands run as the unprivileged user, in a session with the settings the recipe gives it.
	if out := exec(`echo cs-smoke; id -un; hostname; id -nG | tr ' ' '\n' | grep -cx -e sudo -e adm -e root; echo "$http_proxy"`); out.Stdout != "cs-smoke\ncs\ncontrolsnack\n0\nhttp://127.0.0.1:3128\n" {
		t.Errorf("the session: %q", out.Stdout)
	}
	if out := exec(`grep -c 'name="CursorBlink" type="bool" value="false"' /etc/xdg/xfce4/xfconf/xfce-perchannel-xml/xsettings.xml; xfconf-query -c xsettings -p /Net/CursorBlink; for l in zh ja ko; do fc-list ":lang=$l" | wc -l; done`); !strings.HasPrefix(out.Stdout, "1\nfalse\n") || strings.Contains(out.Stdout, "\n0\n") {
		t.Errorf("cursor blink and fonts for Chinese, Japanese and Korean: %q", out.Stdout)
	}

	// A terminal opened with a key combination does not keep the screen from going quiet.
	input(protocol.InputEvent{Type: "key", Keys: "ctrl+alt+t"})
	waitWindow("Xfce4-terminal", 2*time.Minute)
	idle("a new terminal", 30*time.Second)
	shot("01-terminal")
	// Closed the way a person closes it: killing it makes the launcher that started it complain in a dialog.
	input(protocol.InputEvent{Type: "text", Text: "exit\n"})
	for i := 0; i < 20 && byClass("Xfce4-terminal") != nil; i++ {
		time.Sleep(time.Second)
	}

	// An editor: what is typed comes back through the accessibility tree and the recognizer.
	exec(`printf '' > /home/cs/work/smoke.txt; (setsid mousepad /home/cs/work/smoke.txt >/dev/null 2>&1 &); echo launched`)
	editorWindow := waitWindow("Mousepad", 3*time.Minute)
	time.Sleep(15 * time.Second) // it registers with the accessibility service
	focus(editorWindow)
	res := tree(protocol.A11yTreeReq{})
	var editor *protocol.A11yNode
	for i := range res.Nodes {
		if res.Nodes[i].Role == "text" && slices.Contains(res.Nodes[i].States, "editable") {
			editor = &res.Nodes[i]
		}
	}
	if editor == nil {
		t.Fatalf("no editable text in the editor's tree: %+v", res)
	}
	x, y := center(editor.Bounds)
	const typed = "ControlSnack smoke test: ñ é 日本語"
	input(protocol.InputEvent{Type: "click", X: x, Y: y}, protocol.InputEvent{Type: "text", Text: typed})
	time.Sleep(2 * time.Second)
	res = tree(protocol.A11yTreeReq{})
	var got string
	for _, n := range res.Nodes {
		if n.Role == "text" && slices.Contains(n.States, "editable") {
			got = n.Value
		}
	}
	if !strings.Contains(got, typed) {
		t.Errorf("the editor's text through the accessibility tree: %q, want it to contain %q", got, typed)
	}
	idle("the editor", 30*time.Second)
	var ocr protocol.OCRRes
	call(protocol.TypeOCR, protocol.OCRReq{Region: &editorWindow.Bounds, BudgetMS: 60000}, &ocr)
	var seen []string
	for _, l := range ocr.Lines {
		seen = append(seen, l.Text)
	}
	if text := strings.ToLower(strings.Join(seen, "\n")); !strings.Contains(text, "smoke") || !strings.Contains(text, "controlsnack") {
		t.Errorf("recognized text: %q", seen)
	}
	shot("02-editor")

	// A page on disk, with text in several scripts.
	exec(`cat > /home/cs/work/page.html <<'EOF_PAGE'
<!doctype html><html><head><meta charset="utf-8"><title>Smoke Page</title></head><body>
<h1>Smoke page heading</h1><p>Español: ñandú, canción. 日本語のテキスト 中文文本 한국어 텍스트</p></body></html>
EOF_PAGE
echo written`)
	var page protocol.BrowserRes
	call(protocol.TypeBrowser, protocol.BrowserReq{Op: "open", URL: "file:///home/cs/work/page.html"}, &page)
	call(protocol.TypeBrowser, protocol.BrowserReq{Op: "read"}, &page)
	if page.Title != "Smoke Page" || !strings.Contains(page.Text, "Smoke page heading") || !strings.Contains(page.Text, "日本語のテキスト") {
		t.Errorf("the page the browser read: %+v", page)
	}
	time.Sleep(3 * time.Second)
	shot("03-browser")

	// A file manager with a long list: scroll, double click, right click.
	exec(`mkdir -p /home/cs/work/many && cd /home/cs/work/many && for i in $(seq -w 1 150); do : > file$i.txt; done; echo "one" > file001.txt; echo made`)
	exec(`(setsid thunar /home/cs/work/many >/dev/null 2>&1 &); echo launched`)
	files := waitWindow("Thunar", 3*time.Minute)
	time.Sleep(10 * time.Second)
	focus(files)
	before := tree(protocol.A11yTreeReq{})
	top := names(before)
	if !top["file001.txt"] {
		for _, n := range before.Nodes {
			t.Logf("  %-5s %-16s %-30q value=%q bounds=%v", n.ID, n.Role, n.Name, n.Value, n.Bounds)
		}
		t.Fatalf("the first file is listed (%d nodes, truncated=%v, note %q): %v", len(before.Nodes), before.Truncated, before.Note, top)
	}
	var snapBefore protocol.ScreenshotRes
	call(protocol.TypeScreenshot, protocol.ScreenshotReq{Format: "png", MaxLongEdge: 64}, &snapBefore)
	sx, sy := center(files.Bounds)
	input(protocol.InputEvent{Type: "scroll", X: sx, Y: sy, DY: 12})
	time.Sleep(3 * time.Second)
	var snapAfter protocol.ScreenshotRes
	call(protocol.TypeScreenshot, protocol.ScreenshotReq{Format: "png", MaxLongEdge: 64}, &snapAfter)
	scrolled := tree(protocol.A11yTreeReq{})
	if snapAfter.Hash == snapBefore.Hash || (names(scrolled)["file001.txt"] && len(names(scrolled)) == len(top)) {
		t.Errorf("scrolling the list changed nothing: hash %s -> %s", snapBefore.Hash, snapAfter.Hash)
	}
	t.Logf("before the scroll: %d names, after: %d names", len(top), len(names(scrolled)))
	shot("04-scrolled")
	input(protocol.InputEvent{Type: "scroll", X: sx, Y: sy, DY: -20}, protocol.InputEvent{Type: "scroll", X: sx, Y: sy, DY: -20})
	time.Sleep(3 * time.Second)

	// The first file again, then a right click on it: a menu with "Open" appears and Escape closes it.
	res = tree(protocol.A11yTreeReq{})
	var item *protocol.A11yNode
	for i := range res.Nodes {
		if res.Nodes[i].Name == "file001.txt" {
			item = &res.Nodes[i]
		}
	}
	if item == nil {
		t.Fatalf("file001.txt is back in view after scrolling up: %v", names(res))
	}
	ix, iy := center(item.Bounds)
	input(protocol.InputEvent{Type: "click", X: ix, Y: iy, Button: "right"})
	time.Sleep(3 * time.Second)
	menu := tree(protocol.A11yTreeReq{Scope: "desktop", Query: "Open"})
	opens := 0
	for _, n := range menu.Nodes {
		if n.Role == "menu item" && strings.HasPrefix(n.Name, "Open") {
			opens++
		}
	}
	if opens == 0 {
		t.Errorf("a right click shows a menu with Open: %v", names(menu))
	}
	shot("05-menu")
	input(protocol.InputEvent{Type: "key", Keys: "Escape"})
	time.Sleep(2 * time.Second)

	// A double click opens the file in the editor.
	input(protocol.InputEvent{Type: "click", X: ix, Y: iy, Double: true})
	deadline := time.Now().Add(2 * time.Minute)
	opened := false
	for time.Now().Before(deadline) && !opened {
		time.Sleep(3 * time.Second)
		for _, w := range windows() {
			if strings.Contains(w.Title, "file001.txt") && strings.EqualFold(w.Class, "Mousepad") {
				opened = true
			}
		}
	}
	if !opened {
		t.Errorf("a double click on file001.txt does not open it in the editor: %+v", windows())
	}
	shot("06-opened")

	// What one observation costs on the wire, with three windows open (recorded in PROGRESS.md; nothing asserts on it).
	for _, c := range []struct {
		format string
		edge   int
	}{{"webp", 0}, {"webp", 1024}, {"webp", 768}, {"jpeg", 1024}, {"png", 0}} {
		var s protocol.ScreenshotRes
		blob, err := m.Client.Call(ctx, protocol.TypeScreenshot, protocol.ScreenshotReq{Format: c.format, MaxLongEdge: c.edge}, nil, &s)
		if err != nil {
			t.Errorf("screenshot %+v: %v", c, err)
			continue
		}
		t.Logf("payload: screenshot %s, long edge %d (%dx%d): %d bytes", c.format, c.edge, s.W, s.H, len(blob))
	}
	size := func(v any) int {
		b, _ := json.Marshal(v)
		return len(b)
	}
	t.Logf("payload: a11y_tree of the file manager window (%d nodes): %d bytes", len(before.Nodes), size(before))
	for _, max := range []int{150, 400} {
		desk := tree(protocol.A11yTreeReq{Scope: "desktop", MaxNodes: max})
		t.Logf("payload: a11y_tree of the desktop, up to %d nodes (%d returned, truncated %v): %d bytes", max, len(desk.Nodes), desk.Truncated, size(desk))
	}
	t.Logf("payload: ocr of the editor window (%d lines): %d bytes", len(ocr.Lines), size(ocr))
}
