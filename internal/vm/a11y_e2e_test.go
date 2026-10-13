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
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/agent/a11y"
	"github.com/PacifAIst/controlsnack-local/internal/protocol"
	"github.com/PacifAIst/controlsnack-local/internal/vm/agentclient"
)

// roleDump walks the accessibility tree of the whole desktop with gdbus, and prints one line "ROLE|number|name" for
// every role number it meets, the name being what AT-SPI itself says (GetRoleName). The agent's table of role names is
// compared with it.
const roleDump = `
ADDR=$(gdbus call --session --dest org.a11y.Bus --object-path /org/a11y/bus --method org.a11y.Bus.GetAddress | sed "s/^('//; s/',)\$//")
q() { gdbus call --address "$ADDR" --dest "$1" --object-path "$2" --method "$3" 2>/dev/null; }
kids() { q "$1" "$2" org.a11y.atspi.Accessible.GetChildren | grep -oE "\(':[0-9.]+', (objectpath )?'[^']*'\)" | sed -E "s/\(':([0-9.]+)', (objectpath )?'([^']*)'\)/:\1 \3/"; }
declare -A seen
queue=$(kids org.a11y.atspi.Registry /org/a11y/atspi/accessible/root)
n=0
while [ -n "$queue" ] && [ $n -lt 260 ]; do
  line=$(printf '%s\n' "$queue" | head -1)
  queue=$(printf '%s\n' "$queue" | tail -n +2)
  set -- $line
  [ -n "${2:-}" ] || continue
  role=$(q "$1" "$2" org.a11y.atspi.Accessible.GetRole | grep -o 'uint32 [0-9]*' | cut -d' ' -f2)
  if [ -n "$role" ] && [ -z "${seen[$role]:-}" ]; then
    seen[$role]=1
    rname=$(q "$1" "$2" org.a11y.atspi.Accessible.GetRoleName | sed -E "s/^\('(.*)',\)\$/\1/")
    echo "ROLE|$role|$rname"
  fi
  c=$(kids "$1" "$2")
  [ -z "$c" ] || queue="$queue
$c"
  n=$((n+1))
done
echo "WALKED|$n"
`

// TestE2EAccessibility reads the accessibility tree of real GTK3 applications in the real guest, acts on elements by
// their ids, and checks the results against what the applications then show.
func TestE2EAccessibility(t *testing.T) {
	m, _ := bootE2E(t, 1280, 800)
	ctx := context.Background()
	if !slices.Contains(m.Ack.Capabilities, protocol.CapA11y) {
		t.Fatalf("no a11y capability: %v", m.Ack.Capabilities)
	}
	time.Sleep(20 * time.Second)

	exec := func(cmd string, timeout time.Duration) protocol.ExecRes {
		t.Helper()
		var res protocol.ExecRes
		if _, err := m.Client.Call(ctx, protocol.TypeExec, protocol.ExecReq{Command: cmd, TimeoutMS: int(timeout.Milliseconds()), MaxOutput: 64 << 10}, nil, &res); err != nil {
			t.Fatalf("exec: %v", err)
		}
		return res
	}
	windows := func() []protocol.Window {
		t.Helper()
		var res protocol.WindowsRes
		if _, err := m.Client.Call(ctx, protocol.TypeWindows, protocol.WindowsReq{Op: "list"}, nil, &res); err != nil {
			t.Fatal(err)
		}
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
	focus := func(class string) {
		t.Helper()
		w := byClass(class)
		if w == nil {
			t.Fatalf("no %s window: %+v", class, windows())
		}
		if _, err := m.Client.Call(ctx, protocol.TypeWindows, protocol.WindowsReq{Op: "focus", WindowID: w.ID}, nil, nil); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Second)
	}
	tree := func(req protocol.A11yTreeReq) protocol.A11yTreeRes {
		t.Helper()
		if req.BudgetMS == 0 {
			req.BudgetMS = 10000 // without hardware acceleration the same walk takes several times as long
		}
		var res protocol.A11yTreeRes
		begin := time.Now()
		if _, err := m.Client.Call(ctx, protocol.TypeA11yTree, req, nil, &res); err != nil {
			t.Fatalf("a11y_tree %+v: %v", req, err)
		}
		t.Logf("a11y_tree %+v: %d nodes, truncated=%v, generation %s, %s", req, len(res.Nodes), res.Truncated, res.Generation, time.Since(begin).Round(time.Millisecond))
		return res
	}
	act := func(id, gen, op, text string) (protocol.A11yActionRes, error) {
		t.Helper()
		var res protocol.A11yActionRes
		_, err := m.Client.Call(ctx, protocol.TypeA11yAction, protocol.A11yActionReq{ID: id, Generation: gen, Op: op, Text: text}, nil, &res)
		return res, err
	}
	dump := func(res protocol.A11yTreeRes) {
		for _, n := range res.Nodes {
			t.Logf("  %-5s %-16s %-30q value=%q states=%v actions=%v bounds=%v", n.ID, n.Role, n.Name, n.Value, n.States, n.Actions, n.Bounds)
		}
	}
	find := func(res protocol.A11yTreeRes, match func(protocol.A11yNode) bool) *protocol.A11yNode {
		for i := range res.Nodes {
			if match(res.Nodes[i]) {
				return &res.Nodes[i]
			}
		}
		return nil
	}

	// Two applications with windows of their own.
	if r := exec("printf 'first line\\n' > /home/cs/work/note.txt; (setsid mousepad /home/cs/work/note.txt >/dev/null 2>&1 &); (setsid thunar /usr/share/doc >/dev/null 2>&1 &); echo launched", 30*time.Second); r.Stdout != "launched\n" {
		t.Fatalf("%+v", r)
	}
	deadline := time.Now().Add(4 * time.Minute)
	for byClass("Mousepad") == nil || byClass("Thunar") == nil {
		if time.Now().After(deadline) {
			t.Fatalf("the applications did not open: %+v", windows())
		}
		time.Sleep(5 * time.Second)
	}
	time.Sleep(15 * time.Second) // they finish drawing and register with the accessibility service

	// The role names the agent uses are the ones AT-SPI uses.
	begin := time.Now()
	dumped := exec(roleDump, 6*time.Minute)
	t.Logf("role dump: exit %d in %s\n%s", dumped.ExitCode, time.Since(begin).Round(time.Second), dumped.Stdout)
	roles := 0
	for _, line := range strings.Split(dumped.Stdout, "\n") {
		parts := strings.Split(strings.TrimSpace(line), "|")
		if len(parts) != 3 || parts[0] != "ROLE" {
			continue
		}
		n, err := strconv.Atoi(parts[1])
		if err != nil {
			continue
		}
		roles++
		if got := a11y.RoleName(uint32(n)); got != parts[2] { //nolint:gosec // a small role number
			t.Errorf("role %d: the agent says %q, AT-SPI says %q", n, got, parts[2])
		}
	}
	if roles < 8 {
		t.Errorf("only %d distinct roles were seen; the walk is too poor to say anything about the table", roles)
	}

	// The text editor.
	focus("Mousepad")
	res := tree(protocol.A11yTreeReq{})
	dump(res)
	if res.Generation == "" || len(res.Nodes) < 5 || res.Note != "" {
		t.Fatalf("a window of a GTK3 application has an accessibility tree: %+v", res)
	}
	if first := res.Nodes[0]; first.Role != "frame" || !strings.Contains(first.Name, "note.txt") || first.Bounds[2] < 100 {
		t.Errorf("the first element is the window: %+v", first)
	}
	for _, n := range res.Nodes {
		if n.Bounds[2] <= 0 || n.Bounds[3] <= 0 || n.Bounds[0] < -2000 || n.Bounds[0] > 5000 {
			t.Errorf("%s has odd bounds %v", n.ID, n.Bounds)
		}
	}
	editor := find(res, func(n protocol.A11yNode) bool { return n.Role == "text" && slices.Contains(n.States, "editable") })
	if editor == nil {
		t.Fatalf("no editable text among the elements")
	}
	if !strings.Contains(editor.Value, "first line") {
		t.Errorf("the editor's text is in the tree: %q", editor.Value)
	}
	if find(res, func(n protocol.A11yNode) bool { return n.Role == "menu" || n.Role == "menu item" }) == nil {
		t.Errorf("the menu bar is in the tree")
	}
	for _, n := range res.Nodes {
		if n.Role == "button" || n.Role == "menu" || n.Role == "menu item" || n.Role == "toggle button" || n.Role == "check box" {
			if len(n.Actions) == 0 {
				t.Errorf("%s %q (%s) lists no action", n.ID, n.Name, n.Role)
			}
			if slices.Contains(n.Actions, "") || (n.Role == "button" && !slices.ContainsFunc(n.Actions, func(a string) bool { return strings.EqualFold(a, "click") })) {
				t.Errorf("%s %q: actions %q are not AT-SPI action names", n.ID, n.Name, n.Actions)
			}
		}
	}

	// Setting text: the field has it afterwards, whatever way it was done.
	const typed = "héllo ünïcode ✓ 日本語\nsecond line"
	out, err := act(editor.ID, res.Generation, "set_text", typed)
	if err != nil {
		t.Fatalf("set_text: %v", err)
	}
	t.Logf("set_text done by %s", out.Method)
	time.Sleep(time.Second)
	res2 := tree(protocol.A11yTreeReq{})
	editor2 := find(res2, func(n protocol.A11yNode) bool { return n.Role == "text" && slices.Contains(n.States, "editable") })
	if editor2 == nil || editor2.Value != typed {
		t.Errorf("the editor holds %+v, want %q", editor2, typed)
	}
	if res2.Generation == res.Generation {
		t.Errorf("each tree has a generation of its own")
	}
	// An id of the earlier generation still works while it is kept: clearing the field.
	if out, err = act(editor.ID, res.Generation, "set_text", ""); err != nil {
		t.Fatalf("clearing: %v", err)
	}
	t.Logf("clear done by %s", out.Method)
	time.Sleep(time.Second)
	if e := find(tree(protocol.A11yTreeReq{Query: "text"}), func(n protocol.A11yNode) bool { return n.Role == "text" && slices.Contains(n.States, "editable") }); e == nil || e.Value != "" {
		t.Errorf("after clearing: %+v", e)
	}

	// The file manager: a menu is opened by id.
	focus("Thunar")
	files := tree(protocol.A11yTreeReq{})
	dump(files)
	// The set-of-marks overlay: the same screen with and without it differs only around the elements.
	marksCheck(t, m, files)

	menu := find(files, func(n protocol.A11yNode) bool { return (n.Role == "menu" || n.Role == "menu item") && n.Name == "File" })
	if menu == nil {
		t.Fatalf("no File menu in the file manager")
	}
	out, err = act(menu.ID, files.Generation, "click", "")
	if err != nil {
		t.Fatalf("clicking the File menu: %v", err)
	}
	t.Logf("File menu clicked by %s", out.Method)
	time.Sleep(2 * time.Second)
	opened := tree(protocol.A11yTreeReq{Scope: "desktop", MaxNodes: 400})
	if find(opened, func(n protocol.A11yNode) bool { return n.Role == "menu item" && strings.Contains(n.Name, "Close") }) == nil {
		dump(opened)
		t.Errorf("the open menu offers 'Close Window' or similar")
	}
	if _, err := m.Client.Call(ctx, protocol.TypeInput, protocol.InputReq{Events: []protocol.InputEvent{{Type: "key", Keys: "Escape"}}}, nil, nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)

	// A pointer click for an element without an action of its own: a list item selects, it does not open.
	files = tree(protocol.A11yTreeReq{})
	item := find(files, func(n protocol.A11yNode) bool {
		return n.Role == "table cell" || n.Role == "list item" || n.Role == "icon"
	})
	if item == nil {
		t.Logf("no list item to click; the pointer path is covered by the unit tests")
	} else if out, err = act(item.ID, files.Generation, "click", ""); err != nil || out.Method != "pointer" {
		t.Errorf("clicking %s %q: %+v %v", item.ID, item.Name, out, err)
	}

	// Scopes.
	desktop := tree(protocol.A11yTreeReq{Scope: "desktop", MaxNodes: 400})
	frames := 0
	for _, n := range desktop.Nodes {
		if n.Role == "frame" || n.Role == "window" {
			frames++
		}
	}
	if frames < 2 {
		t.Errorf("the desktop has at least the editor and the file manager: %d windows", frames)
	}
	if q := tree(protocol.A11yTreeReq{Query: "file"}); len(q.Nodes) == 0 || len(q.Nodes) >= len(files.Nodes) {
		t.Errorf("a query narrows the tree: %d of %d", len(q.Nodes), len(files.Nodes))
	}
	if few := tree(protocol.A11yTreeReq{MaxNodes: 5}); len(few.Nodes) != 5 || !few.Truncated {
		t.Errorf("max_nodes cuts: %d truncated=%v", len(few.Nodes), few.Truncated)
	}

	// Refusals.
	var re *agentclient.RemoteError
	if _, err := act("a1", "g999", "click", ""); !errors.As(err, &re) || re.Code != protocol.CodeStaleElement {
		t.Errorf("an unknown generation is stale: %v", err)
	}
	if _, err := act("a399", desktop.Generation, "click", ""); !errors.As(err, &re) || re.Code != protocol.CodeNotFound {
		t.Errorf("an id the generation does not have: %v", err)
	}
	if _, err := act("a1", desktop.Generation, "hover", ""); !errors.As(err, &re) || re.Code != protocol.CodeInvalidArgs {
		t.Errorf("an unknown operation: %v", err)
	}
	if _, err := m.Client.Call(ctx, protocol.TypeA11yTree, protocol.A11yTreeReq{Scope: "browser"}, nil, nil); !errors.As(err, &re) || re.Code != protocol.CodeInvalidArgs {
		t.Errorf("the browser scope is the browser tool's: %v", err)
	}

	// An element that closed is stale, not a crash: close the editor and use its old id.
	exec("pkill -x mousepad; sleep 1; echo closed", 30*time.Second)
	time.Sleep(3 * time.Second)
	if _, err := act(editor.ID, res.Generation, "set_text", "gone"); !errors.As(err, &re) || re.Code != protocol.CodeStaleElement {
		t.Errorf("an element of a closed application: %v", err)
	}
}

// marksCheck takes the screen twice, plain and with the marks of a tree's generation, and checks that the marks are
// drawn: many pixels differ, nearly all of them next to an element of the tree.
func marksCheck(t *testing.T, m *Machine, tree protocol.A11yTreeRes) {
	t.Helper()
	ctx := context.Background()
	shoot := func(req protocol.ScreenshotReq) image.Image {
		t.Helper()
		var res protocol.ScreenshotRes
		blob, err := m.Client.Call(ctx, protocol.TypeScreenshot, req, nil, &res)
		if err != nil {
			t.Fatalf("screenshot %+v: %v", req, err)
		}
		img, err := png.Decode(bytes.NewReader(blob))
		if err != nil {
			t.Fatal(err)
		}
		return img
	}
	plain := shoot(protocol.ScreenshotReq{Format: "png"})
	marked := shoot(protocol.ScreenshotReq{Format: "png", MarksGeneration: tree.Generation})
	if plain.Bounds() != marked.Bounds() {
		t.Fatalf("marks change the picture, not its size: %v vs %v", plain.Bounds(), marked.Bounds())
	}
	near := func(x, y int) bool {
		for _, n := range tree.Nodes {
			if x >= n.Bounds[0]-24 && x < n.Bounds[0]+n.Bounds[2]+24 && y >= n.Bounds[1]-24 && y < n.Bounds[1]+n.Bounds[3]+24 {
				return true
			}
		}
		return false
	}
	inside, outside := 0, 0
	b := plain.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if plain.At(x, y) != marked.At(x, y) {
				if near(x, y) {
					inside++
				} else {
					outside++
				}
			}
		}
	}
	t.Logf("marks: %d pixels differ next to elements, %d elsewhere", inside, outside)
	if inside < 300 {
		t.Errorf("the marks are not drawn (%d pixels differ)", inside)
	}
	if outside > 400 { // the panel's clock may tick between the two pictures
		t.Errorf("%d pixels changed away from every element", outside)
	}
	if _, err := m.Client.Call(ctx, protocol.TypeScreenshot, protocol.ScreenshotReq{Format: "png", MarksGeneration: "g99999"}, nil, nil); err == nil {
		t.Error("the marks of a generation that is not kept")
	}
}
