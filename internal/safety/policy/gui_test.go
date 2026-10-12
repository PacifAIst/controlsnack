// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package policy

import (
	"strings"
	"testing"

	"github.com/PacifAIst/controlsnack-local/internal/safety"
)

func testSnapshot() *A11ySnapshot {
	return &A11ySnapshot{Nodes: []A11yNode{
		{ID: "a0", Role: "frame", Name: "Delete files?", Bounds: [4]int{0, 0, 800, 600}},
		{ID: "a1", Role: "push button", Name: "Delete", Bounds: [4]int{100, 100, 80, 30}, Actions: []string{"click"}},
		{ID: "a2", Role: "push button", Name: "Cancel", Bounds: [4]int{200, 100, 80, 30}, Actions: []string{"click"}},
		{ID: "a3", Role: "menu item", Name: "Empty Trash", Bounds: [4]int{300, 100, 100, 20}},
		{ID: "a4", Role: "push button", Name: "Send message", Bounds: [4]int{100, 200, 100, 30}, Actions: []string{"click"}},
		{ID: "a5", Role: "password text", Name: "Password", Bounds: [4]int{100, 300, 200, 30}, States: []string{"focused", "showing"}},
		{ID: "a6", Role: "entry", Name: "Username", Bounds: [4]int{100, 350, 200, 30}},
		{ID: "a7", Role: "push button", Name: "OK", Description: "Permanently remove the selected files", Bounds: [4]int{400, 400, 60, 30}, Actions: []string{"click"}},
		{ID: "a8", Role: "panel", Name: "Delete all", Bounds: [4]int{600, 100, 200, 200}, Actions: []string{"click"}},
		{ID: "a9", Role: "push button", Name: "Save", Bounds: [4]int{650, 150, 60, 30}, Actions: []string{"click"}},
		{ID: "a10", Role: "link", Name: "Log in", Bounds: [4]int{10, 500, 60, 20}},
		{ID: "w5", Role: "push button", Name: "Remove item", Bounds: [4]int{500, 500, 80, 30}, Actions: []string{"click"}},
		{ID: "w6", Role: "password text", Name: "Pass", Bounds: [4]int{500, 550, 80, 30}},
		{ID: "w7", Role: "push button", Name: "Subscribe now", Bounds: [4]int{500, 450, 80, 30}},
		{ID: "a11", Role: "push button", Name: "Reformat", Bounds: [4]int{10, 10, 10, 10}, Actions: []string{"click"}}, // "format" only inside a word
		{ID: "a12", Role: "push button", Name: "Postpone", Bounds: [4]int{30, 10, 10, 10}, Actions: []string{"click"}}, // "post" only inside a word
	}}
}

func guiIDs(d GUIDecision) string { return sortedIDs(d.Decision) }

func TestGUIClicksAndTextByLabel(t *testing.T) {
	snap := testSnapshot()
	e := mustEngine(t, "", Options{})
	net := mustEngine(t, "", Options{NetEgress: true})
	tests := []struct {
		name      string
		a         GUIAction
		off, on   string // rule IDs without and with --allow-net
		wantRedat bool
	}{
		{"delete button", GUIAction{Tool: "click_element", ID: "a1"}, "CONFIRM-GUI-DESTRUCTIVE", "CONFIRM-GUI-DESTRUCTIVE", false},
		{"cancel button", GUIAction{Tool: "click_element", ID: "a2"}, "", "", false},
		{"empty trash menu item", GUIAction{Tool: "click_element", ID: "a3"}, "CONFIRM-GUI-DESTRUCTIVE", "CONFIRM-GUI-DESTRUCTIVE", false},
		{"label hidden in the description", GUIAction{Tool: "click_element", ID: "a7"}, "CONFIRM-GUI-DESTRUCTIVE", "CONFIRM-GUI-DESTRUCTIVE", false},
		{"send is only risky with network", GUIAction{Tool: "click_element", ID: "a4"}, "", "CONFIRM-GUI-NET-ACTION", false},
		{"log in is only risky with network", GUIAction{Tool: "click_element", ID: "a10"}, "", "CONFIRM-GUI-NET-ACTION", false},
		{"web remove button", GUIAction{Tool: "browser", Op: "click", ID: "w5"}, "CONFIRM-GUI-DESTRUCTIVE", "CONFIRM-GUI-DESTRUCTIVE", false},
		{"web subscribe button", GUIAction{Tool: "browser", Op: "click", ID: "w7"}, "", "CONFIRM-GUI-NET-ACTION", false},
		{"words inside words do not count (Reformat)", GUIAction{Tool: "click_element", ID: "a11"}, "", "", false},
		{"words inside words do not count (Postpone)", GUIAction{Tool: "click_element", ID: "a12"}, "", "", false},
		{"unknown element: nothing to judge", GUIAction{Tool: "click_element", ID: "a999"}, "", "", false},
		{"set_text into a plain field", GUIAction{Tool: "set_text", ID: "a6", Text: "bob"}, "", "", false},
		{"set_text into a password field", GUIAction{Tool: "set_text", ID: "a5", Text: "hunter2"}, "CONFIRM-PASSWORD-FIELD", "CONFIRM-PASSWORD-FIELD", true},
		{"set_text on a delete-labeled element", GUIAction{Tool: "set_text", ID: "a1", Text: "x"}, "CONFIRM-GUI-DESTRUCTIVE", "CONFIRM-GUI-DESTRUCTIVE", false},
		{"type_text goes to the focused password field", GUIAction{Tool: "type_text", Text: "hunter2"}, "CONFIRM-PASSWORD-FIELD", "CONFIRM-PASSWORD-FIELD", true},
		{"browser type into a password node", GUIAction{Tool: "browser", Op: "type", ID: "w6", Text: "x"}, "CONFIRM-PASSWORD-FIELD", "CONFIRM-PASSWORD-FIELD", true},
		{"browser type into a plain node", GUIAction{Tool: "browser", Op: "type", ID: "a6", Text: "x"}, "", "", false},
		{"browser type by password selector", GUIAction{Tool: "browser", Op: "type", Target: `input[type=password]`, Text: "x"}, "CONFIRM-PASSWORD-FIELD", "CONFIRM-PASSWORD-FIELD", true},
		{"browser type by quoted selector", GUIAction{Tool: "browser", Op: "type", Target: `form INPUT[ TYPE = "password" ]`, Text: "x"}, "CONFIRM-PASSWORD-FIELD", "CONFIRM-PASSWORD-FIELD", true},
		{"browser type by text selector", GUIAction{Tool: "browser", Op: "type", Target: `input[type=text]`, Text: "x"}, "", "", false},
		{"an unknown tool is not judged here", GUIAction{Tool: "exec"}, "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := guiIDs(e.EvaluateGUI(tt.a, snap)); got != tt.off {
				t.Errorf("without --allow-net: %q, want %q", got, tt.off)
			}
			d := net.EvaluateGUI(tt.a, snap)
			if got := guiIDs(d); got != tt.on {
				t.Errorf("with --allow-net: %q, want %q", got, tt.on)
			}
			if d.RedactText != tt.wantRedat {
				t.Errorf("RedactText = %v, want %v", d.RedactText, tt.wantRedat)
			}
			if d.Verdict != wantVerdict(net, tt.on) {
				t.Errorf("verdict %v does not match rules %q", d.Verdict, tt.on)
			}
		})
	}
	// Without a snapshot there is nothing to read labels from.
	if got := guiIDs(e.EvaluateGUI(GUIAction{Tool: "click_element", ID: "a1"}, nil)); got != "" {
		t.Errorf("nil snapshot: %q", got)
	}
	if got := guiIDs(e.EvaluateGUI(GUIAction{Tool: "mouse", Op: "click", X: 120, Y: 110}, nil)); got != "" {
		t.Errorf("nil snapshot, mouse: %q", got)
	}
}

func TestMouseClicksAreHitTested(t *testing.T) {
	snap := testSnapshot()
	e := mustEngine(t, "", Options{})
	tests := []struct {
		name string
		a    GUIAction
		want string
	}{
		{"click on Delete", GUIAction{Tool: "mouse", Op: "click", X: 120, Y: 110}, "CONFIRM-GUI-DESTRUCTIVE"},
		{"double click on Delete", GUIAction{Tool: "mouse", Op: "double_click", X: 100, Y: 100}, "CONFIRM-GUI-DESTRUCTIVE"},
		{"right click on Delete", GUIAction{Tool: "mouse", Op: "right_click", X: 179, Y: 129}, "CONFIRM-GUI-DESTRUCTIVE"},
		{"mouse down on Delete", GUIAction{Tool: "mouse", Op: "down", X: 120, Y: 110}, "CONFIRM-GUI-DESTRUCTIVE"},
		{"just outside the button", GUIAction{Tool: "mouse", Op: "click", X: 180, Y: 110}, ""},
		{"click on Cancel", GUIAction{Tool: "mouse", Op: "click", X: 220, Y: 110}, ""},
		{"moving over Delete is harmless", GUIAction{Tool: "mouse", Op: "move", X: 120, Y: 110}, ""},
		{"blank area of a dialog titled Delete files", GUIAction{Tool: "mouse", Op: "click", X: 500, Y: 50}, ""},
		{"empty trash", GUIAction{Tool: "mouse", Op: "click", X: 350, Y: 110}, "CONFIRM-GUI-DESTRUCTIVE"},
		{"the innermost element wins (Save inside Delete all)", GUIAction{Tool: "mouse", Op: "click", X: 660, Y: 160}, ""},
		{"the outer element when not on the inner", GUIAction{Tool: "mouse", Op: "click", X: 610, Y: 110}, "CONFIRM-GUI-DESTRUCTIVE"},
		{"drag onto Delete", GUIAction{Tool: "mouse", Op: "drag", X: 220, Y: 110, ToX: 120, ToY: 110}, "CONFIRM-GUI-DESTRUCTIVE"},
		{"drag from Delete", GUIAction{Tool: "mouse", Op: "drag", X: 120, Y: 110, ToX: 220, ToY: 110}, "CONFIRM-GUI-DESTRUCTIVE"},
		{"drag between harmless things", GUIAction{Tool: "mouse", Op: "drag", X: 220, Y: 110, ToX: 300, ToY: 300}, ""},
		{"coordinates far outside", GUIAction{Tool: "mouse", Op: "click", X: -5, Y: 99999}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := guiIDs(e.EvaluateGUI(tt.a, snap)); got != tt.want {
				t.Errorf("%q, want %q", got, tt.want)
			}
		})
	}
}

func TestShiftDelete(t *testing.T) {
	e := mustEngine(t, "", Options{})
	yes := []string{"shift+Delete", "Shift+delete", "SHIFT+DELETE", "shift+KP_Delete", "Shift_L+Delete", "ctrl+shift+Delete", "shift+ctrl+Delete", " shift + Delete "}
	no := []string{"Delete", "ctrl+Delete", "shift+Return", "shift+BackSpace", "alt+F4", "ctrl+a", "ctrl+shift+t", "shift", "", "Delete+shift"}
	for _, k := range yes {
		if got := guiIDs(e.EvaluateGUI(GUIAction{Tool: "key", Keys: k}, nil)); got != "CONFIRM-GUI-DESTRUCTIVE" {
			t.Errorf("key %q: %q", k, got)
		}
	}
	for _, k := range no {
		if got := guiIDs(e.EvaluateGUI(GUIAction{Tool: "key", Keys: k}, nil)); got != "" {
			t.Errorf("key %q must be allowed, got %q", k, got)
		}
	}
}

func TestBrowserURLs(t *testing.T) {
	e := mustEngine(t, "", Options{})
	ok := []string{
		"http://example.com", "https://example.com/a?b=c#d", "HTTPS://EXAMPLE.COM", "file:///home/cs/work/index.html",
		"example.com", "example.com/path", "localhost:8080/x", "127.0.0.1:3000", "localhost:8080", " https://example.com ",
		"https://user@example.com", "//example.com/x", "/home/cs/x.html", "www.example.com:443/a",
	}
	bad := []string{
		"javascript:alert(1)", "JaVaScRiPt:alert(1)", "java\tscript:alert(1)", "java\nscript:alert(1)", " javascript:alert(1)",
		"\x00javascript:alert(1)", "data:text/html,<script>alert(1)</script>", "DATA:text/html;base64,AAAA", "chrome://settings",
		"chrome-extension://abc/x", "devtools://devtools/bundled/inspector.html", "view-source:http://example.com", "ftp://example.com/x",
		"about:blank", "blob:http://example.com/uuid", "ws://example.com", "mailto:a@b.c", "localhost:abc", "x:y", "", "   ", "javascript://%0aalert(1)",
	}
	for _, u := range ok {
		for _, op := range []string{"open", "navigate"} {
			if got := guiIDs(e.EvaluateGUI(GUIAction{Tool: "browser", Op: op, URL: u}, nil)); got != "" {
				t.Errorf("browser %s %q must be allowed, got %q", op, u, got)
			}
		}
	}
	for _, u := range bad {
		d := e.EvaluateGUI(GUIAction{Tool: "browser", Op: "navigate", URL: u}, nil)
		if guiIDs(d) != "DENY-BROWSER-URL" || d.Verdict != safety.Deny {
			t.Errorf("browser navigate %q must be denied, got %q (%v)", u, guiIDs(d), d.Verdict)
		}
	}
	// Other browser ops carry no URL to judge.
	if got := guiIDs(e.EvaluateGUI(GUIAction{Tool: "browser", Op: "back"}, nil)); got != "" {
		t.Errorf("browser back: %q", got)
	}
}

func TestGUIRulesFollowUserOverrides(t *testing.T) {
	e := mustEngine(t, "override:\n  CONFIRM-GUI-DESTRUCTIVE: {class: allow}\n  CONFIRM-PASSWORD-FIELD: {class: deny}\n", Options{})
	snap := testSnapshot()
	if got := guiIDs(e.EvaluateGUI(GUIAction{Tool: "click_element", ID: "a1"}, snap)); got != "" {
		t.Errorf("a disabled GUI rule must not match: %q", got)
	}
	d := e.EvaluateGUI(GUIAction{Tool: "set_text", ID: "a5", Text: "x"}, snap)
	if d.Verdict != safety.Deny || !d.RedactText {
		t.Errorf("an overridden class applies and redaction stays on: %+v", d)
	}
	relaxed := mustEngine(t, "override:\n  DENY-BROWSER-URL: {class: confirm}\n", Options{})
	if d := relaxed.EvaluateGUI(GUIAction{Tool: "browser", Op: "open", URL: "about:blank"}, nil); d.Verdict != safety.Confirm {
		t.Errorf("override of DENY-BROWSER-URL: %v", d.Verdict)
	}
}

func TestSnapshotHelpers(t *testing.T) {
	var nilSnap *A11ySnapshot
	if _, ok := nilSnap.Lookup("a1"); ok {
		t.Error("nil snapshot lookup")
	}
	if _, ok := nilSnap.Focused(); ok {
		t.Error("nil snapshot focus")
	}
	if _, ok := nilSnap.hitTest(1, 1); ok {
		t.Error("nil snapshot hit test")
	}
	s := testSnapshot()
	if n, ok := s.Focused(); !ok || n.ID != "a5" {
		t.Errorf("focused = %+v", n)
	}
	if n, ok := s.Lookup("w5"); !ok || !strings.Contains(n.Name, "Remove") {
		t.Errorf("lookup = %+v", n)
	}
	if (A11yNode{Bounds: [4]int{0, 0, 0, 10}}).contains(0, 0) {
		t.Error("a zero-width node contains nothing")
	}
}
