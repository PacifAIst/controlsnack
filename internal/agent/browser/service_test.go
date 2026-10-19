// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package browser

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/PacifAIst/controlsnack/internal/protocol"
)

func newTestService() (*Service, *fakeBrowser) {
	f := newFake()
	f.page()
	f.text = "Welcome\nDocs"
	s := NewService(f)
	s.Settle = 0
	return s, f
}

func kindOf(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return -1
}

func do(t *testing.T, s *Service, req protocol.BrowserReq) protocol.BrowserRes {
	t.Helper()
	res, err := s.Do(context.Background(), req)
	if err != nil {
		t.Fatalf("%+v: %v", req, err)
	}
	return res
}

func TestCheckURL(t *testing.T) {
	good := []string{
		"http://example.com", "https://example.com/a/b?c=d#e", "http://127.0.0.1:8080/", "file:///home/cs/work/page.html",
		"file://localhost/tmp/x.html", "HTTPS://EXAMPLE.COM/", "  https://example.com/  ", "http://[::1]:3000/",
	}
	for _, u := range good {
		if _, err := CheckURL(u); err != nil {
			t.Errorf("%q: %v", u, err)
		}
	}
	bad := []string{
		"", "   ", "example.com", "//example.com", "javascript:alert(1)", "JavaScript:alert(1)", "data:text/html,<h1>x</h1>",
		"chrome://settings", "devtools://devtools/bundled/inspector.html", "view-source:http://example.com", "about:blank",
		"ftp://example.com/", "blob:http://example.com/x", "filesystem:http://example.com/temporary/x",
		"http://", "https:///path", "http://user:pass@example.com/", "http://user@example.com/", "file://remote/share/x", "file://",
		"http://exa mple.com/", "http://example.com/\nHost: evil", "http://example.com/\x00", "http://example.com/" + strings.Repeat("a", 3000),
		"http://example.com/\xff", "http://%zz/",
	}
	for _, u := range bad {
		if _, err := CheckURL(u); err == nil || kindOf(err) != KindInvalid {
			t.Errorf("%q should be refused as invalid: %v", u, err)
		}
	}
	if got, _ := CheckURL("  https://example.com/x  "); got != "https://example.com/x" {
		t.Errorf("the address is trimmed: %q", got)
	}
}

func TestCheckSelectorAndText(t *testing.T) {
	for _, s := range []string{"#id", "a[href^='http']", "form > input:nth-child(2)", "div.a, div.b"} {
		if err := CheckSelector(s); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
	for _, s := range []string{"", "  ", "a\nb", "a\x00", strings.Repeat("a", 501), "\xff"} {
		if err := CheckSelector(s); kindOf(err) != KindInvalid {
			t.Errorf("%q: %v", s, err)
		}
	}
	if err := CheckText("line one\n\tline two ✓ 日本語"); err != nil {
		t.Error(err)
	}
	for _, s := range []string{"a\x07b", "\x1b[2J", "bad \xff", strings.Repeat("x", MaxTypeRunes+1)} {
		if err := CheckText(s); kindOf(err) != KindInvalid {
			t.Errorf("%q: %v", s, err)
		}
	}
	if err := CheckText(strings.Repeat("é", MaxTypeRunes)); err != nil {
		t.Errorf("the limit counts characters, not bytes: %v", err)
	}
}

func TestEachOperationTakesOnlyItsOwnFields(t *testing.T) {
	s, f := newTestService()
	for name, req := range map[string]protocol.BrowserReq{
		"unknown op":         {Op: "close"},
		"empty op":           {},
		"back with url":      {Op: "back", URL: "https://example.com"},
		"reload with target": {Op: "reload", Target: "#a"},
		"click with text":    {Op: "click", Target: "#a", Text: "x"},
		"click with submit":  {Op: "click", Target: "#a", Submit: true},
		"navigate with tab":  {Op: "navigate", URL: "https://example.com", Tab: 2},
		"open with query":    {Op: "open", Query: "x"},
		"read with nodes":    {Op: "read", MaxNodes: 5},
		"tabs with target":   {Op: "tabs", Target: "#a"},
		"switch with url":    {Op: "switch_tab", Tab: 1, URL: "https://example.com"},
	} {
		if _, err := s.Do(context.Background(), req); kindOf(err) != KindInvalid {
			t.Errorf("%s: %v", name, err)
		}
	}
	if f.logged() != "" {
		t.Errorf("a refused request did something: %s", f.logged())
	}
}

func TestOpenNavigateAndHistory(t *testing.T) {
	s, f := newTestService()
	res := do(t, s, protocol.BrowserReq{Op: "open", URL: "https://example.com/one"})
	if res.URL != "https://example.com/one" || res.Title != "Page https://example.com/one" {
		t.Errorf("%+v", res)
	}
	do(t, s, protocol.BrowserReq{Op: "navigate", URL: "file:///home/cs/work/two.html"})
	if res = do(t, s, protocol.BrowserReq{Op: "back"}); res.URL != "https://example.com/one" {
		t.Errorf("back: %+v", res)
	}
	if res = do(t, s, protocol.BrowserReq{Op: "forward"}); res.URL != "file:///home/cs/work/two.html" {
		t.Errorf("forward: %+v", res)
	}
	if _, err := s.Do(context.Background(), protocol.BrowserReq{Op: "forward"}); kindOf(err) != KindNotFound || !strings.Contains(err.Error(), "forward") {
		t.Errorf("no later page: %v", err)
	}
	do(t, s, protocol.BrowserReq{Op: "back"})
	if _, err := s.Do(context.Background(), protocol.BrowserReq{Op: "back"}); kindOf(err) != KindNotFound || !strings.Contains(err.Error(), "back") {
		t.Errorf("no earlier page: %v", err)
	}
	do(t, s, protocol.BrowserReq{Op: "reload"})
	if res = do(t, s, protocol.BrowserReq{Op: "open"}); res.URL != "about:blank" {
		t.Errorf("open without an address opens a blank page: %+v", res)
	}
	for _, u := range []string{"javascript:alert(1)", "data:text/html,x", "about:blank", ""} {
		if _, err := s.Do(context.Background(), protocol.BrowserReq{Op: "navigate", URL: u}); kindOf(err) != KindInvalid {
			t.Errorf("navigate %q: %v", u, err)
		}
	}
	if _, err := s.Do(context.Background(), protocol.BrowserReq{Op: "open", URL: "javascript:1"}); kindOf(err) != KindInvalid {
		t.Errorf("open javascript: %v", err)
	}
	if strings.Contains(f.logged(), "javascript") || strings.Contains(f.logged(), "data:") {
		t.Errorf("a refused address reached the browser: %s", f.logged())
	}
}

func TestTabs(t *testing.T) {
	s, _ := newTestService()
	if res := do(t, s, protocol.BrowserReq{Op: "tabs"}); len(res.Tabs) != 0 || !strings.Contains(res.Note, "open") {
		t.Errorf("no browser, no tabs, and a way forward: %+v", res)
	}
	do(t, s, protocol.BrowserReq{Op: "open", URL: "https://example.com/a"})
	do(t, s, protocol.BrowserReq{Op: "open", URL: "https://example.com/b"})
	res := do(t, s, protocol.BrowserReq{Op: "tabs"})
	want := []protocol.BrowserTab{
		{Index: 1, Title: "Page https://example.com/a", URL: "https://example.com/a"},
		{Index: 2, Title: "Page https://example.com/b", URL: "https://example.com/b", Active: true},
	}
	if !slices.Equal(res.Tabs, want) {
		t.Errorf("got %+v\nwant %+v", res.Tabs, want)
	}
	if got := do(t, s, protocol.BrowserReq{Op: "switch_tab", Tab: 1}); got.URL != "https://example.com/a" {
		t.Errorf("switch: %+v", got)
	}
	if tabs := do(t, s, protocol.BrowserReq{Op: "tabs"}).Tabs; !tabs[0].Active || tabs[1].Active {
		t.Errorf("%+v", tabs)
	}
	for _, n := range []int{0, 3, 99} {
		if _, err := s.Do(context.Background(), protocol.BrowserReq{Op: "switch_tab", Tab: n}); kindOf(err) != KindInvalid {
			t.Errorf("tab %d: %v", n, err)
		}
	}
}

func TestRead(t *testing.T) {
	s, f := newTestService()
	f.text = "Welcome\r\n\tto the \x1b[31mpage\x07\x00"
	do(t, s, protocol.BrowserReq{Op: "open", URL: "https://example.com/"})
	res := do(t, s, protocol.BrowserReq{Op: "read"})
	if res.Text != "Welcome\n\tto the [31mpage" || res.Truncated || res.URL != "https://example.com/" {
		t.Errorf("%+v", res)
	}
	f.text = strings.Repeat("日本語", 6000)
	res = do(t, s, protocol.BrowserReq{Op: "read"})
	if len(res.Text) > MaxReadBytes || !res.Truncated || !utf8.ValidString(res.Text) || len(res.Text) < MaxReadBytes-4 {
		t.Errorf("%d bytes, truncated=%v, valid=%v", len(res.Text), res.Truncated, utf8.ValidString(res.Text))
	}
	f.text = "x"
	f.tabs[0].title = "A title\nwith\x07 junk"
	if res = do(t, s, protocol.BrowserReq{Op: "read"}); res.Title != "A title with junk" {
		t.Errorf("titles are cleaned: %q", res.Title)
	}
}

func TestAXTree(t *testing.T) {
	s, _ := newTestService()
	do(t, s, protocol.BrowserReq{Op: "open", URL: "https://example.com/"})
	res := do(t, s, protocol.BrowserReq{Op: "ax_tree"})
	if res.Generation != "g1" || res.Truncated || res.Note != "" || len(res.Nodes) != 5 {
		t.Fatalf("%+v", res)
	}
	want := []protocol.A11yNode{
		{ID: "w1", Role: "heading", Name: "Welcome", Bounds: protocol.Rect{10, 20, 200, 30}},
		{ID: "w2", Role: "link", Name: "Docs", Bounds: protocol.Rect{10, 60, 50, 20}, Actions: []string{"click"}},
		{ID: "w3", Role: "button", Name: "Save", States: []string{"focused"}, Bounds: protocol.Rect{10, 100, 60, 24}, Actions: []string{"click"}},
		{ID: "w4", Role: "textbox", Name: "Search", Value: "cats", Bounds: protocol.Rect{80, 100, 150, 24}, Actions: []string{"type"}},
		{ID: "w5", Role: "password text", Name: "Password", Bounds: protocol.Rect{80, 140, 150, 24}, Actions: []string{"type"}},
	}
	for i, w := range want {
		g := res.Nodes[i]
		if g.ID != w.ID || g.Role != w.Role || g.Name != w.Name || g.Value != w.Value || g.Bounds != w.Bounds ||
			!slices.Equal(g.States, w.States) || !slices.Equal(g.Actions, w.Actions) {
			t.Errorf("node %d: %+v, want %+v", i, g, w)
		}
	}
	if res2 := do(t, s, protocol.BrowserReq{Op: "ax_tree", Query: "SAVE"}); res2.Generation != "g2" || len(res2.Nodes) != 1 || res2.Nodes[0].ID != "w1" || res2.Nodes[0].Name != "Save" {
		t.Errorf("a query narrows, and ids start again: %+v", res2)
	}
	if res3 := do(t, s, protocol.BrowserReq{Op: "ax_tree", MaxNodes: 2}); len(res3.Nodes) != 2 || !res3.Truncated {
		t.Errorf("%+v", res3)
	}
	if res4 := do(t, s, protocol.BrowserReq{Op: "ax_tree", MaxNodes: 5}); len(res4.Nodes) != 5 || res4.Truncated {
		t.Errorf("exactly the limit is not truncated: %+v", res4)
	}
	if res5 := do(t, s, protocol.BrowserReq{Op: "ax_tree", Query: "nothing like this"}); len(res5.Nodes) != 0 || res5.Note == "" {
		t.Errorf("%+v", res5)
	}
	for name, req := range map[string]protocol.BrowserReq{
		"negative":   {Op: "ax_tree", MaxNodes: -1},
		"too many":   {Op: "ax_tree", MaxNodes: 401},
		"long query": {Op: "ax_tree", Query: strings.Repeat("q", 101)},
	} {
		if _, err := s.Do(context.Background(), req); kindOf(err) != KindInvalid {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestAXTreeFindsEnoughRenderedElementsAmongManyThatAreNot(t *testing.T) {
	s, f := newTestService()
	do(t, s, protocol.BrowserReq{Op: "open", URL: "https://example.com/"})
	f.nodes, f.boxes = nil, map[int64]Box{}
	for i := range 450 { // 400 elements that are not on the page, then 50 that are
		n := AXNode{Node: int64(1000 + i), Role: "button", Name: fmt.Sprintf("B%d", i)}
		f.nodes = append(f.nodes, n)
		if i >= 400 {
			f.boxes[n.Node] = Box{i, i, 10, 10}
		}
	}
	res := do(t, s, protocol.BrowserReq{Op: "ax_tree", MaxNodes: 30})
	if len(res.Nodes) != 30 || !res.Truncated || res.Nodes[0].Name != "B400" {
		t.Errorf("%d nodes, truncated=%v, first %q", len(res.Nodes), res.Truncated, res.Nodes[0].Name)
	}
}

func TestClickByIdChecksTheElementFirst(t *testing.T) {
	s, f := newTestService()
	do(t, s, protocol.BrowserReq{Op: "open", URL: "https://example.com/"})
	do(t, s, protocol.BrowserReq{Op: "ax_tree"})
	res := do(t, s, protocol.BrowserReq{Op: "click", Target: "w2"})
	if res.URL != "https://example.com/" || !strings.Contains(f.logged(), "click node 12") {
		t.Errorf("%+v %s", res, f.logged())
	}
	do(t, s, protocol.BrowserReq{Op: "type", Target: "w4", Text: "dogs", Submit: true})
	if !strings.Contains(f.logged(), `type node 14 "dogs" submit=true`) {
		t.Errorf("%s", f.logged())
	}

	// The element was renamed: not what the model was shown.
	f.names[12] = "Elsewhere"
	if _, err := s.Do(context.Background(), protocol.BrowserReq{Op: "click", Target: "w2"}); kindOf(err) != KindStale || !strings.Contains(err.Error(), "changed") {
		t.Errorf("renamed: %v", err)
	}
	delete(f.names, 12)
	// The element left the page.
	f.gone[13] = true
	if _, err := s.Do(context.Background(), protocol.BrowserReq{Op: "click", Target: "w3"}); kindOf(err) != KindStale || !strings.Contains(err.Error(), "gone") {
		t.Errorf("gone: %v", err)
	}
	if strings.Contains(f.logged(), "click node 13") {
		t.Error("a stale element was clicked")
	}
	// An id the tree does not have.
	if _, err := s.Do(context.Background(), protocol.BrowserReq{Op: "click", Target: "w99"}); kindOf(err) != KindNotFound {
		t.Errorf("unknown id: %v", err)
	}
}

func TestIdsAreStaleOnAnotherTabAndBeforeAnyTree(t *testing.T) {
	s, f := newTestService()
	if _, err := s.Do(context.Background(), protocol.BrowserReq{Op: "click", Target: "w1"}); kindOf(err) != KindStale {
		t.Errorf("no tree yet: %v", err)
	}
	do(t, s, protocol.BrowserReq{Op: "open", URL: "https://example.com/a"})
	do(t, s, protocol.BrowserReq{Op: "ax_tree"})
	do(t, s, protocol.BrowserReq{Op: "open", URL: "https://example.com/b"}) // a new tab is in front
	if _, err := s.Do(context.Background(), protocol.BrowserReq{Op: "click", Target: "w1"}); kindOf(err) != KindStale || !strings.Contains(err.Error(), "another tab") {
		t.Errorf("the tree is of another tab: %v", err)
	}
	do(t, s, protocol.BrowserReq{Op: "switch_tab", Tab: 1})
	do(t, s, protocol.BrowserReq{Op: "click", Target: "w1"}) // back on the tab the tree is of
	if !strings.Contains(f.logged(), "click node 11") {
		t.Errorf("%s", f.logged())
	}
}

func TestClickAndTypeBySelector(t *testing.T) {
	s, f := newTestService()
	do(t, s, protocol.BrowserReq{Op: "open", URL: "https://example.com/"})
	do(t, s, protocol.BrowserReq{Op: "click", Target: "#save"})
	do(t, s, protocol.BrowserReq{Op: "type", Target: "input[name=q]", Text: "héllo\nworld"})
	if want := `open https://example.com/; click #save; type input[name=q] "héllo\nworld" submit=false`; f.logged() != want {
		t.Errorf("got %s\nwant %s", f.logged(), want)
	}
	for name, req := range map[string]protocol.BrowserReq{
		"no target":        {Op: "click"},
		"selector control": {Op: "click", Target: "a\x00b"},
		"long selector":    {Op: "click", Target: strings.Repeat("a", 501)},
		"type no target":   {Op: "type", Text: "x"},
		"type control":     {Op: "type", Target: "#a", Text: "a\x07"},
		"type too long":    {Op: "type", Target: "#a", Text: strings.Repeat("x", MaxTypeRunes+1)},
	} {
		if _, err := s.Do(context.Background(), req); kindOf(err) != KindInvalid {
			t.Errorf("%s: %v", name, err)
		}
	}
	f.err["ClickSelector"] = ErrNoMatch
	if _, err := s.Do(context.Background(), protocol.BrowserReq{Op: "click", Target: "#nothing"}); kindOf(err) != KindNotFound {
		t.Errorf("%v", err)
	}
	f.err["ClickSelector"] = ErrBadSelector
	if _, err := s.Do(context.Background(), protocol.BrowserReq{Op: "click", Target: "##"}); kindOf(err) != KindInvalid {
		t.Errorf("%v", err)
	}
}

func TestErrorsFromTheBrowserCarryTheRightKinds(t *testing.T) {
	for name, tt := range map[string]struct {
		err  error
		kind Kind
		text string
	}{
		"not open":    {ErrNoBrowser, KindNotFound, "not open"},
		"unavailable": {fmt.Errorf("%w: no such file", ErrUnavailable), KindUnsupported, "cannot be started"},
		"gone":        {ErrNodeGone, KindStale, "gone"},
		"deadline":    {context.DeadlineExceeded, KindTimeout, "took too long"},
		"other":       {errors.New("net::ERR_CONNECTION_REFUSED\x1b[31m at http://x/"), KindFailed, "ERR_CONNECTION_REFUSED"},
	} {
		s, f := newTestService()
		do(t, s, protocol.BrowserReq{Op: "open", URL: "https://example.com/"})
		f.err["Navigate"] = tt.err
		_, err := s.Do(context.Background(), protocol.BrowserReq{Op: "navigate", URL: "https://example.com/x"})
		if kindOf(err) != tt.kind || !strings.Contains(err.Error(), tt.text) || strings.ContainsAny(err.Error(), "\x1b") {
			t.Errorf("%s: %v", name, err)
		}
	}
	s, f := newTestService()
	if _, err := s.Do(context.Background(), protocol.BrowserReq{Op: "read"}); kindOf(err) != KindNotFound {
		t.Errorf("read with no browser: %v", err)
	}
	f.err["Open"] = ErrUnavailable
	if _, err := s.Do(context.Background(), protocol.BrowserReq{Op: "open", URL: "https://example.com/"}); kindOf(err) != KindUnsupported {
		t.Errorf("open when chromium is missing: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.err["Open"] = context.Canceled
	if _, err := s.Do(ctx, protocol.BrowserReq{Op: "open", URL: "https://example.com/"}); !errors.Is(err, context.Canceled) {
		t.Errorf("a canceled request stays a cancellation: %v", err)
	}
}

func TestSettleWaitsAfterAClickAndStopsWhenCanceled(t *testing.T) {
	s, _ := newTestService()
	s.Settle = 150 * time.Millisecond
	do(t, s, protocol.BrowserReq{Op: "open", URL: "https://example.com/"})
	begin := time.Now()
	do(t, s, protocol.BrowserReq{Op: "click", Target: "#a"})
	if took := time.Since(begin); took < 140*time.Millisecond {
		t.Errorf("returned after %v", took)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	if _, err := s.Do(ctx, protocol.BrowserReq{Op: "click", Target: "#a"}); !errors.Is(err, context.Canceled) {
		t.Errorf("%v", err)
	}
}

func TestConcurrentTreesAndClicks(t *testing.T) {
	s, _ := newTestService()
	do(t, s, protocol.BrowserReq{Op: "open", URL: "https://example.com/"})
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = s.Do(context.Background(), protocol.BrowserReq{Op: "ax_tree"})
		}()
		go func() {
			defer wg.Done()
			_, _ = s.Do(context.Background(), protocol.BrowserReq{Op: "click", Target: "w1"})
		}()
	}
	wg.Wait()
	if res := do(t, s, protocol.BrowserReq{Op: "ax_tree"}); res.Generation != "g21" {
		t.Errorf("each tree takes the next generation: %s", res.Generation)
	}
}

func TestCutBytes(t *testing.T) {
	if got := cutBytes("日本語", 4); got != "日" {
		t.Errorf("%q", got)
	}
	if got := cutBytes("abc", 10); got != "abc" {
		t.Errorf("%q", got)
	}
	if got := cutBytes("日", 0); got != "" {
		t.Errorf("%q", got)
	}
}
