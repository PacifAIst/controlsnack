// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package policy

import (
	"regexp"
	"sort"
	"strings"
)

// A11yNode is one element of the accessibility snapshot the model last saw (PLAN.md section 5.3). The policy
// uses it to learn what a click would land on.
type A11yNode struct {
	ID          string
	Role        string
	Name        string
	Description string
	// Bounds is x, y, width, height in guest pixels.
	Bounds  [4]int
	States  []string
	Actions []string
}

// A11ySnapshot is the latest accessibility tree.
type A11ySnapshot struct {
	Nodes []A11yNode
}

// Lookup returns the node with the given ID.
func (s *A11ySnapshot) Lookup(id string) (A11yNode, bool) {
	if s == nil {
		return A11yNode{}, false
	}
	for _, n := range s.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return A11yNode{}, false
}

// Focused returns the node that has keyboard focus, if the snapshot says which.
func (s *A11ySnapshot) Focused() (A11yNode, bool) {
	if s == nil {
		return A11yNode{}, false
	}
	for _, n := range s.Nodes {
		for _, st := range n.States {
			if st == "focused" {
				return n, true
			}
		}
	}
	return A11yNode{}, false
}

// interactiveRoles are the roles a click usually does something on, whether or not the tree lists actions.
var interactiveRoles = map[string]bool{
	"push button": true, "toggle button": true, "check box": true, "radio button": true, "menu item": true,
	"check menu item": true, "radio menu item": true, "link": true, "list item": true, "tab": true,
	"combo box": true, "entry": true, "page tab": true, "tree item": true, "table cell": true,
	"button": true, "menuitem": true, "checkbox": true, "radio": true, "tab item": true, "icon": true,
}

func (n A11yNode) interactive() bool {
	return len(n.Actions) > 0 || interactiveRoles[strings.ToLower(n.Role)]
}

func (n A11yNode) area() int { return n.Bounds[2] * n.Bounds[3] }

func (n A11yNode) contains(x, y int) bool {
	return n.Bounds[2] > 0 && n.Bounds[3] > 0 &&
		x >= n.Bounds[0] && x < n.Bounds[0]+n.Bounds[2] && y >= n.Bounds[1] && y < n.Bounds[1]+n.Bounds[3]
}

// hitTest returns the innermost interactive element under a point.
func (s *A11ySnapshot) hitTest(x, y int) (A11yNode, bool) {
	if s == nil {
		return A11yNode{}, false
	}
	var best A11yNode
	found := false
	for _, n := range s.Nodes {
		if !n.interactive() || !n.contains(x, y) {
			continue
		}
		if !found || n.area() < best.area() {
			best, found = n, true
		}
	}
	return best, found
}

// GUIAction is the part of a model action the GUI rules look at. The session fills it from the validated action
// (internal/llm/actions) after mapping image coordinates to guest pixels.
type GUIAction struct {
	// Tool is click_element, set_text, mouse, key, type_text or browser.
	Tool string
	// Op is the mouse op (click, double_click, right_click, middle_click, drag, down, up, move) or the browser op.
	Op string
	// ID is the element id for click_element and set_text, or a w-id for browser actions.
	ID string
	// Target is a CSS selector for browser actions.
	Target string
	// Text is what set_text, type_text or browser type will enter (only its presence matters to the rules).
	Text string
	// Keys is the key combination of a key action.
	Keys string
	// URL is the address of browser open and navigate.
	URL string
	// X, Y is the mouse position; ToX, ToY the end of a drag.
	X, Y, ToX, ToY int
}

// GUIDecision is a Decision plus whether the text typed must be hidden in the viewer and audit log.
type GUIDecision struct {
	Decision
	RedactText bool
}

var (
	reDestructive = regexp.MustCompile(`(?i)\b(delete|remove|erase|discard|format|overwrite|replace all|empty trash|uninstall)\b`)
	reNetAction   = regexp.MustCompile(`(?i)\b(send|submit|post|publish|pay|purchase|buy|order|checkout|transfer|subscribe|sign up|log in|sign in)\b`)
	rePasswordSel = regexp.MustCompile(`(?i)type\s*=\s*["']?password`)
	reScheme      = regexp.MustCompile(`^([a-zA-Z][a-zA-Z0-9+.\-]*):`)
	reHostPort    = regexp.MustCompile(`^[0-9]+(/|\?|#|$)`)
)

// EvaluateGUI judges a GUI or browser action. snap is the accessibility snapshot the model last saw; it may be
// nil, in which case only the rules that need no snapshot (key combinations, browser URLs, selectors) apply.
//
// An element that is not in the snapshot cannot be judged by its label; the action then runs into the
// stale_element check of the executor rather than being second-guessed here.
func (e *Engine) EvaluateGUI(a GUIAction, snap *A11ySnapshot) GUIDecision {
	g := &analysis{e: e, seen: map[string]bool{}}
	var redact bool

	label := func(n A11yNode) string { return n.Name + " " + n.Description }
	judgeLabel := func(n A11yNode, what string) {
		text := label(n)
		inv := what + " " + strings.TrimSpace(n.Role+" "+quote(n.Name))
		if reDestructive.MatchString(text) {
			g.emit("CONFIRM-GUI-DESTRUCTIVE", inv)
		}
		if reNetAction.MatchString(text) {
			g.emit("CONFIRM-GUI-NET-ACTION", inv)
		}
	}
	isPassword := func(n A11yNode) bool { return strings.EqualFold(n.Role, "password text") }

	switch a.Tool {
	case "click_element":
		if n, ok := snap.Lookup(a.ID); ok {
			judgeLabel(n, "click")
		}

	case "set_text":
		if n, ok := snap.Lookup(a.ID); ok {
			judgeLabel(n, "set text of")
			if isPassword(n) {
				g.emit("CONFIRM-PASSWORD-FIELD", "set text of "+quote(n.Name))
				redact = true
			}
		}

	case "type_text":
		if n, ok := snap.Focused(); ok && isPassword(n) {
			g.emit("CONFIRM-PASSWORD-FIELD", "type into "+quote(n.Name))
			redact = true
		}

	case "mouse":
		switch a.Op {
		case "click", "double_click", "right_click", "middle_click", "down", "up":
			if n, ok := snap.hitTest(a.X, a.Y); ok {
				judgeLabel(n, "click at")
			}
		case "drag":
			if n, ok := snap.hitTest(a.X, a.Y); ok {
				judgeLabel(n, "drag from")
			}
			if n, ok := snap.hitTest(a.ToX, a.ToY); ok {
				judgeLabel(n, "drop on")
			}
		}

	case "key":
		if isShiftDelete(a.Keys) {
			g.emit("CONFIRM-GUI-DESTRUCTIVE", "key "+a.Keys+" (permanent delete)")
		}

	case "browser":
		switch a.Op {
		case "open", "navigate":
			if !allowedBrowserURL(a.URL) {
				g.emit("DENY-BROWSER-URL", "browser "+a.Op+" "+truncate(a.URL, 120))
			}
		case "click":
			if n, ok := snap.Lookup(a.ID); ok {
				judgeLabel(n, "browser click")
			}
		case "type":
			if n, ok := snap.Lookup(a.ID); ok && isPassword(n) {
				g.emit("CONFIRM-PASSWORD-FIELD", "browser type into "+quote(n.Name))
				redact = true
			}
			if a.Target != "" && rePasswordSel.MatchString(a.Target) {
				g.emit("CONFIRM-PASSWORD-FIELD", "browser type into "+truncate(a.Target, 80))
				redact = true
			}
		}
	}

	return GUIDecision{Decision: g.decision(), RedactText: redact}
}

func quote(s string) string { return `"` + truncate(s, 60) + `"` }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}

// isShiftDelete reports whether a key combination is shift+Delete in any capitalization or keypad form.
func isShiftDelete(keys string) bool {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(keys)), "+")
	if len(parts) < 2 {
		return false
	}
	norm := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(p), "_l"), "_r")
		norm = append(norm, p)
	}
	last := norm[len(norm)-1]
	if last != "delete" && last != "kp_delete" && last != "del" {
		return false
	}
	mods := norm[:len(norm)-1]
	sort.Strings(mods)
	for _, m := range mods {
		if m == "shift" {
			return true
		}
	}
	return false
}

// allowedBrowserURL accepts http, https and file URLs. Characters browsers silently drop inside a scheme (tabs,
// newlines, spaces) are removed first so "java\tscript:" cannot slip through, and any other scheme is refused.
// An address without a scheme ("example.com", "localhost:8080/x") is treated as http(s).
func allowedBrowserURL(raw string) bool {
	var b strings.Builder
	for _, r := range raw {
		if r > 0x20 && r != 0x7f {
			b.WriteRune(r)
		}
	}
	u := b.String()
	if u == "" {
		return false
	}
	m := reScheme.FindStringSubmatch(u)
	if m == nil {
		return true
	}
	switch strings.ToLower(m[1]) {
	case "http", "https", "file":
		return true
	}
	// "host:8080/path" looks like a scheme to the regexp but is an address with a port.
	return reHostPort.MatchString(u[len(m[0]):])
}
