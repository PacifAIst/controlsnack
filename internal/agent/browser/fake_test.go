// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package browser

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// fakeBrowser is a browser in memory: tabs with a history each, a page with elements, and a record of what was done.
type fakeBrowser struct {
	mu sync.Mutex

	running bool
	tabs    []*fakeTab
	active  int // index in tabs

	// what the page has
	nodes []AXNode
	boxes map[int64]Box
	text  string
	gone  map[int64]bool // elements that have left the page
	names map[int64]string

	// what was done
	log []string

	// errors to give
	err      map[string]error // by method name
	lastType string
}

type fakeTab struct {
	id      string
	history []string
	at      int
	title   string
}

func newFake() *fakeBrowser {
	return &fakeBrowser{err: map[string]error{}, boxes: map[int64]Box{}, gone: map[int64]bool{}, names: map[int64]string{}}
}

func (f *fakeBrowser) record(s string) { f.log = append(f.log, s) }

func (f *fakeBrowser) fail(method string) error { return f.err[method] }

func (f *fakeBrowser) current() (*fakeTab, error) {
	if !f.running || len(f.tabs) == 0 {
		return nil, ErrNoBrowser
	}
	return f.tabs[f.active], nil
}

func (t *fakeTab) url() string { return t.history[t.at] }

func titleOf(url string) string { return "Page " + url }

func (f *fakeBrowser) Tabs(context.Context) ([]Tab, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("Tabs"); err != nil {
		return nil, err
	}
	out := make([]Tab, len(f.tabs))
	for i, t := range f.tabs {
		out[i] = Tab{ID: t.id, Title: titleOf(t.url()), URL: t.url(), Active: i == f.active}
		if t.title != "" {
			out[i].Title = t.title
		}
	}
	return out, nil
}

func (f *fakeBrowser) Open(_ context.Context, url string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("Open"); err != nil {
		return err
	}
	if url == "" {
		url = "about:blank"
	}
	f.running = true
	f.tabs = append(f.tabs, &fakeTab{id: fmt.Sprintf("T%d", len(f.tabs)+1), history: []string{url}})
	f.active = len(f.tabs) - 1
	f.record("open " + url)
	return nil
}

func (f *fakeBrowser) Activate(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("Activate"); err != nil {
		return err
	}
	for i, t := range f.tabs {
		if t.id == id {
			f.active = i
			f.record("activate " + id)
			return nil
		}
	}
	return ErrNoBrowser
}

func (f *fakeBrowser) Navigate(_ context.Context, url string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("Navigate"); err != nil {
		return err
	}
	t, err := f.current()
	if err != nil {
		return err
	}
	t.history = append(t.history[:t.at+1], url)
	t.at++
	f.record("navigate " + url)
	return nil
}

func (f *fakeBrowser) History(_ context.Context, delta int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("History"); err != nil {
		return err
	}
	t, err := f.current()
	if err != nil {
		return err
	}
	if t.at+delta < 0 || t.at+delta >= len(t.history) {
		return ErrNoHistory
	}
	t.at += delta
	f.record(fmt.Sprintf("history %+d", delta))
	return nil
}

func (f *fakeBrowser) Reload(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("Reload"); err != nil {
		return err
	}
	if _, err := f.current(); err != nil {
		return err
	}
	f.record("reload")
	return nil
}

func (f *fakeBrowser) Summary(context.Context) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("Summary"); err != nil {
		return "", "", err
	}
	t, err := f.current()
	if err != nil {
		return "", "", err
	}
	if t.title != "" {
		return t.title, t.url(), nil
	}
	return titleOf(t.url()), t.url(), nil
}

func (f *fakeBrowser) VisibleText(_ context.Context, max int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("VisibleText"); err != nil {
		return "", err
	}
	if _, err := f.current(); err != nil {
		return "", err
	}
	return f.text, nil
}

func (f *fakeBrowser) AXNodes(context.Context) (Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("AXNodes"); err != nil {
		return Snapshot{}, err
	}
	t, err := f.current()
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Tab: t.id, Nodes: append([]AXNode(nil), f.nodes...)}, nil
}

func (f *fakeBrowser) Boxes(_ context.Context, nodes []int64) (map[int64]Box, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("Boxes"); err != nil {
		return nil, err
	}
	out := map[int64]Box{}
	for _, n := range nodes {
		if b, ok := f.boxes[n]; ok {
			out[n] = b
		}
	}
	return out, nil
}

func (f *fakeBrowser) Describe(_ context.Context, node int64) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("Describe"); err != nil {
		return "", "", err
	}
	if f.gone[node] {
		return "", "", ErrNodeGone
	}
	for _, n := range f.nodes {
		if n.Node == node {
			if name, ok := f.names[node]; ok {
				return n.Role, name, nil
			}
			return n.Role, n.Name, nil
		}
	}
	return "", "", ErrNodeGone
}

func (f *fakeBrowser) ClickNode(_ context.Context, node int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("ClickNode"); err != nil {
		return err
	}
	f.record(fmt.Sprintf("click node %d", node))
	return nil
}

func (f *fakeBrowser) ClickSelector(_ context.Context, sel string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("ClickSelector"); err != nil {
		return err
	}
	f.record("click " + sel)
	return nil
}

func (f *fakeBrowser) TypeNode(_ context.Context, node int64, text string, submit bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("TypeNode"); err != nil {
		return err
	}
	f.lastType = text
	f.record(fmt.Sprintf("type node %d %q submit=%v", node, text, submit))
	return nil
}

func (f *fakeBrowser) TypeSelector(_ context.Context, sel, text string, submit bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("TypeSelector"); err != nil {
		return err
	}
	f.lastType = text
	f.record(fmt.Sprintf("type %s %q submit=%v", sel, text, submit))
	return nil
}

func (f *fakeBrowser) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.running = false
	f.tabs = nil
	f.record("close")
	return nil
}

func (f *fakeBrowser) logged() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.log, "; ")
}

// page puts a small page in the fake: a heading, a link, a button, a field (a password field too) and an element that is
// not rendered.
func (f *fakeBrowser) page() {
	f.nodes = []AXNode{
		{Node: 11, Role: "heading", Name: "Welcome"},
		{Node: 12, Role: "link", Name: "Docs"},
		{Node: 13, Role: "button", Name: "Save", States: []string{"focused"}},
		{Node: 14, Role: "textbox", Name: "Search", Value: "cats"},
		{Node: 15, Role: "password text", Name: "Password"},
		{Node: 16, Role: "button", Name: "Hidden"},
	}
	f.boxes = map[int64]Box{
		11: {10, 20, 200, 30}, 12: {10, 60, 50, 20}, 13: {10, 100, 60, 24}, 14: {80, 100, 150, 24}, 15: {80, 140, 150, 24},
		// 16 has no box: it is not rendered.
	}
}
