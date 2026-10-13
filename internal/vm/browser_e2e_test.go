// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e

package vm

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/protocol"
	"github.com/PacifAIst/controlsnack-local/internal/vm/agentclient"
)

// The pages of the test, written into the guest by the test.
const (
	pageOne = `<!doctype html><html><head><title>CS Test Page</title></head><body>
<h1>Test page heading</h1>
<p id="status">idle</p>
<a id="next" href="page2.html">Go to page two</a>
<button id="btn" onclick="document.getElementById('status').textContent='clicked'">Press me</button>
<label>Name <input id="name" name="q" type="text" value="old value"></label>
<label>Secret <input id="pw" type="password" value="hunter2"></label>
<form onsubmit="document.getElementById('status').textContent='submitted:'+document.getElementById('q2').value; return false;">
<input id="q2" type="text" aria-label="Second field"></form>
<label>Notes <textarea id="notes"></textarea></label>
<div style="height:3000px"></div>
<button id="far" onclick="document.getElementById('status').textContent='far clicked'">Far away</button>
</body></html>`
	pageTwo = `<!doctype html><html><head><title>Page Two</title></head><body>
<h1>Second page</h1><a href="index.html">Back to one</a></body></html>`
)

// TestE2EBrowser drives the real guest's Chromium through the agent's `browser` request: opening local pages, reading
// them, the accessibility tree of a page, clicking and typing by id and by selector, navigation, tabs, stale ids and
// refusals; and it compares where the agent says an element is with where the desktop's accessibility service says.
func TestE2EBrowser(t *testing.T) {
	m, dir := bootE2E(t, 1280, 800)
	ctx := context.Background()
	if !slices.Contains(m.Ack.Capabilities, protocol.CapBrowser) {
		t.Fatalf("no browser capability: %v", m.Ack.Capabilities)
	}
	time.Sleep(20 * time.Second)

	var res protocol.ExecRes
	cmd := "mkdir -p /home/cs/work/site && cat > /home/cs/work/site/index.html <<'EOF_ONE'\n" + pageOne + "\nEOF_ONE\n" +
		"cat > /home/cs/work/site/page2.html <<'EOF_TWO'\n" + pageTwo + "\nEOF_TWO\necho written"
	if _, err := m.Client.Call(ctx, protocol.TypeExec, protocol.ExecReq{Command: cmd}, nil, &res); err != nil || res.Stdout != "written\n" {
		t.Fatalf("%+v %v", res, err)
	}
	const base = "file:///home/cs/work/site/"

	call := func(req protocol.BrowserReq) (protocol.BrowserRes, error) {
		t.Helper()
		var out protocol.BrowserRes
		_, err := m.Client.Call(ctx, protocol.TypeBrowser, req, nil, &out)
		return out, err
	}
	must := func(req protocol.BrowserReq) protocol.BrowserRes {
		t.Helper()
		begin := time.Now()
		out, err := call(req)
		if err != nil {
			t.Fatalf("browser %+v: %v", req, err)
		}
		t.Logf("browser %s %s%s: %s", req.Op, req.URL, req.Target, time.Since(begin).Round(time.Millisecond))
		return out
	}
	code := func(err error) string {
		var re *agentclient.RemoteError
		if errors.As(err, &re) {
			return re.Code
		}
		return ""
	}
	find := func(nodes []protocol.A11yNode, role, name string) *protocol.A11yNode {
		for i := range nodes {
			if nodes[i].Role == role && nodes[i].Name == name {
				return &nodes[i]
			}
		}
		return nil
	}

	// Before the browser is open.
	if out := must(protocol.BrowserReq{Op: "tabs"}); len(out.Tabs) != 0 || out.Note == "" {
		t.Errorf("no browser, no tabs: %+v", out)
	}
	if _, err := call(protocol.BrowserReq{Op: "read"}); code(err) != protocol.CodeNotFound {
		t.Errorf("read with no browser: %v", err)
	}

	// Opening starts Chromium.
	out := must(protocol.BrowserReq{Op: "open", URL: base + "index.html"})
	if out.Title != "CS Test Page" || out.URL != base+"index.html" {
		t.Fatalf("%+v", out)
	}
	time.Sleep(5 * time.Second)
	if keep := os.Getenv("CONTROLSNACK_E2E_SHOTS"); keep != "" {
		p := filepath.Join(dir, "browser.png")
		if err := m.Screendump(ctx, p); err == nil {
			if data, err := os.ReadFile(p); err == nil {
				_ = os.WriteFile(filepath.Join(keep, "browser.png"), data, 0o600)
			}
		}
	}

	// Reading.
	read := must(protocol.BrowserReq{Op: "read"})
	for _, want := range []string{"Test page heading", "idle", "Go to page two", "Press me", "Far away"} {
		if !strings.Contains(read.Text, want) {
			t.Errorf("the page shows %q; read gave %q", want, read.Text)
		}
	}

	// The accessibility tree of the page.
	tree := must(protocol.BrowserReq{Op: "ax_tree"})
	t.Logf("ax_tree: generation %s, %d nodes, truncated=%v", tree.Generation, len(tree.Nodes), tree.Truncated)
	for _, n := range tree.Nodes {
		t.Logf("  %-5s %-14s %-24q value=%q states=%v bounds=%v", n.ID, n.Role, n.Name, n.Value, n.States, n.Bounds)
	}
	heading := find(tree.Nodes, "heading", "Test page heading")
	link := find(tree.Nodes, "link", "Go to page two")
	button := find(tree.Nodes, "button", "Press me")
	name := find(tree.Nodes, "textbox", "Name")
	password := find(tree.Nodes, "password text", "Secret")
	second := find(tree.Nodes, "textbox", "Second field")
	if heading == nil || link == nil || button == nil || name == nil || password == nil || second == nil {
		t.Fatalf("elements missing from the tree: heading %v link %v button %v name %v password %v second %v", heading, link, button, name, password, second)
	}
	if name.Value != "old value" {
		t.Errorf("the field's value: %q", name.Value)
	}
	if password.Value != "" {
		t.Errorf("a password field's value must never be reported: %q", password.Value)
	}
	if b, _ := json.Marshal(tree); strings.Contains(string(b), "hunter2") {
		t.Errorf("the password appears in the answer")
	}
	if find(tree.Nodes, "button", "Far away") == nil {
		t.Errorf("an element below the fold is in the tree: it is on the page")
	}
	for _, n := range tree.Nodes {
		if n.Bounds[2] <= 0 || n.Bounds[3] <= 0 {
			t.Errorf("%s has no area: %v", n.ID, n.Bounds)
		}
	}

	// Where the agent puts an element against where the desktop's accessibility service does.
	var desk protocol.A11yTreeRes
	if _, err := m.Client.Call(ctx, protocol.TypeA11yTree, protocol.A11yTreeReq{Scope: "desktop", Query: "Press me", BudgetMS: 30000, MaxNodes: 50}, nil, &desk); err == nil {
		if n := find(desk.Nodes, "push button", "Press me"); n != nil || len(desk.Nodes) > 0 {
			for _, d := range desk.Nodes {
				if d.Name != "Press me" {
					continue
				}
				dx := (d.Bounds[0] + d.Bounds[2]/2) - (button.Bounds[0] + button.Bounds[2]/2)
				dy := (d.Bounds[1] + d.Bounds[3]/2) - (button.Bounds[1] + button.Bounds[3]/2)
				t.Logf("the button: agent %v, accessibility service %v (centers differ by %d, %d)", button.Bounds, d.Bounds, dx, dy)
				if dx < -20 || dx > 20 || dy < -20 || dy > 20 {
					t.Errorf("the approximate place of a web element is off by (%d, %d) pixels", dx, dy)
				}
			}
		} else {
			t.Logf("the desktop tree does not show the page's button (%d nodes): no comparison", len(desk.Nodes))
		}
	}

	// Clicking by id, typing by id.
	must(protocol.BrowserReq{Op: "click", Target: button.ID})
	if read = must(protocol.BrowserReq{Op: "read"}); !strings.Contains(read.Text, "clicked") {
		t.Errorf("the click did not run the page's script: %q", read.Text)
	}
	must(protocol.BrowserReq{Op: "type", Target: name.ID, Text: "new text"})
	again := must(protocol.BrowserReq{Op: "ax_tree", Query: "Name"})
	if n := find(again.Nodes, "textbox", "Name"); n == nil || n.Value != "new text" {
		t.Errorf("typing replaces what the field held: %+v", n)
	}
	// Typing into the password field works, and its value stays out of the tree. The ids are those of the last tree
	// read (the narrowed one above), so the whole tree is read again for the field's id.
	full := must(protocol.BrowserReq{Op: "ax_tree"})
	pw := find(full.Nodes, "password text", "Secret")
	if pw == nil {
		t.Fatalf("the password field: %+v", full.Nodes)
	}
	must(protocol.BrowserReq{Op: "type", Target: pw.ID, Text: "s3cret"})
	afterTyping := must(protocol.BrowserReq{Op: "ax_tree", Query: "Secret"})
	if b, _ := json.Marshal(afterTyping); strings.Contains(string(b), "s3cret") || strings.Contains(string(b), "hunter2") {
		t.Errorf("a password field's value leaked into the tree: %s", b)
	}
	// By selector, with submit.
	must(protocol.BrowserReq{Op: "type", Target: "#q2", Text: "abc", Submit: true})
	if read = must(protocol.BrowserReq{Op: "read"}); !strings.Contains(read.Text, "submitted:abc") {
		t.Errorf("Enter submitted the form: %q", read.Text)
	}
	// A line break goes into a text area.
	must(protocol.BrowserReq{Op: "type", Target: "#notes", Text: "line one\nline two"})
	// An element below the fold is scrolled to.
	must(protocol.BrowserReq{Op: "click", Target: "#far"})
	if read = must(protocol.BrowserReq{Op: "read"}); !strings.Contains(read.Text, "far clicked") {
		t.Errorf("the far button: %q", read.Text)
	}

	// Navigation by link, history, reload.
	scrolled := must(protocol.BrowserReq{Op: "ax_tree", Query: "Go to page two"})
	l2 := find(scrolled.Nodes, "link", "Go to page two")
	if l2 == nil {
		t.Fatalf("the link: %+v", scrolled.Nodes)
	}
	if out = must(protocol.BrowserReq{Op: "click", Target: l2.ID}); out.URL != base+"page2.html" || out.Title != "Page Two" {
		t.Errorf("clicking a link goes to the page: %+v", out)
	}
	if _, err := call(protocol.BrowserReq{Op: "click", Target: l2.ID}); code(err) != protocol.CodeStaleElement {
		t.Errorf("an id of the page that was left is stale: %v", err)
	}
	if out = must(protocol.BrowserReq{Op: "back"}); out.URL != base+"index.html" {
		t.Errorf("back: %+v", out)
	}
	if out = must(protocol.BrowserReq{Op: "forward"}); out.URL != base+"page2.html" {
		t.Errorf("forward: %+v", out)
	}
	if _, err := call(protocol.BrowserReq{Op: "forward"}); code(err) != protocol.CodeNotFound {
		t.Errorf("no later page: %v", err)
	}
	must(protocol.BrowserReq{Op: "reload"})
	if out = must(protocol.BrowserReq{Op: "navigate", URL: base + "index.html"}); out.Title != "CS Test Page" {
		t.Errorf("navigate: %+v", out)
	}

	// Tabs.
	if out = must(protocol.BrowserReq{Op: "open", URL: base + "page2.html"}); out.Title != "Page Two" {
		t.Errorf("a second tab: %+v", out)
	}
	tabs := must(protocol.BrowserReq{Op: "tabs"}).Tabs
	if len(tabs) != 2 || !tabs[1].Active || tabs[0].Active || tabs[0].Title != "CS Test Page" || tabs[1].Title != "Page Two" {
		t.Errorf("tabs: %+v", tabs)
	}
	tree2 := must(protocol.BrowserReq{Op: "ax_tree"})
	if out = must(protocol.BrowserReq{Op: "switch_tab", Tab: 1}); out.Title != "CS Test Page" {
		t.Errorf("switch_tab: %+v", out)
	}
	if len(tree2.Nodes) > 0 {
		if _, err := call(protocol.BrowserReq{Op: "click", Target: tree2.Nodes[0].ID}); code(err) != protocol.CodeStaleElement {
			t.Errorf("an id from another tab: %v", err)
		}
	}

	// Refusals.
	for name, req := range map[string]protocol.BrowserReq{
		"javascript url":   {Op: "navigate", URL: "javascript:alert(1)"},
		"data url":         {Op: "open", URL: "data:text/html,<h1>x</h1>"},
		"chrome url":       {Op: "navigate", URL: "chrome://settings"},
		"bad selector":     {Op: "click", Target: "##"},
		"tab out of range": {Op: "switch_tab", Tab: 9},
		"unknown op":       {Op: "close"},
	} {
		if _, err := call(req); code(err) != protocol.CodeInvalidArgs {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := call(protocol.BrowserReq{Op: "click", Target: "#no-such-element"}); code(err) != protocol.CodeNotFound {
		t.Errorf("no element matches: %v", err)
	}
	// Without the network a web address cannot be reached: the proxy is not there. The answer says so.
	_, err := call(protocol.BrowserReq{Op: "navigate", URL: "http://example.com/"})
	t.Logf("navigating to a web address without the network: %v", err)
	if err == nil {
		t.Errorf("a web address cannot load without the network")
	}
}
