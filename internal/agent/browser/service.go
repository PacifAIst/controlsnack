// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

// Package browser drives the guest's Chromium through the DevTools protocol: tabs, navigation, reading a page, the
// accessibility tree of a page, and clicking and typing in it by id or by selector.
package browser

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/PacifAIst/controlsnack/internal/agent/textclean"
	"github.com/PacifAIst/controlsnack/internal/protocol"
)

// Operations of a browser request.
const (
	OpOpen      = "open"
	OpNavigate  = "navigate"
	OpBack      = "back"
	OpForward   = "forward"
	OpReload    = "reload"
	OpClick     = "click"
	OpType      = "type"
	OpRead      = "read"
	OpAXTree    = "ax_tree"
	OpTabs      = "tabs"
	OpSwitchTab = "switch_tab"
)

// Limits of the answers.
const (
	DefaultMaxNodes = 150
	MaxMaxNodes     = 400
	maxQueryRunes   = 100
	// MaxReadBytes is the most page text one read returns (PLAN.md section 8).
	MaxReadBytes = 16 << 10
	// boxBatch is how many elements are asked about at once when looking for the ones that are on the page.
	boxBatch = 100
	// defaultSettle is how long to wait after a click or typing before looking at the page again: a click may start a
	// navigation, and the address read at once would be the old one.
	defaultSettle = 400 * time.Millisecond
)

var reNodeID = regexp.MustCompile(`^w[0-9]{1,5}$`)

// Service carries out browser requests over a Backend.
type Service struct {
	b Backend
	// Settle is the wait after a click or typing (defaultSettle; tests make it zero).
	Settle time.Duration

	mu    sync.Mutex
	gen   int
	table *table
}

// table is the last accessibility tree: what each id stands for. Ids are valid for that tree only.
type table struct {
	tab   string
	nodes map[string]entry
}

type entry struct {
	node       int64
	role, name string
}

// NewService returns a service over a backend.
func NewService(b Backend) *Service { return &Service{b: b, Settle: defaultSettle} }

// Close ends the browser.
func (s *Service) Close() error { return s.b.Close() }

// fields a request may carry, by operation: anything else is refused, so a host that sends the wrong thing hears of it.
type fields struct{ url, target, text, submit, tab, tree bool }

var allowed = map[string]fields{
	OpOpen:      {url: true},
	OpNavigate:  {url: true},
	OpBack:      {},
	OpForward:   {},
	OpReload:    {},
	OpClick:     {target: true},
	OpType:      {target: true, text: true, submit: true},
	OpRead:      {},
	OpAXTree:    {tree: true},
	OpTabs:      {},
	OpSwitchTab: {tab: true},
}

func checkFields(req protocol.BrowserReq) error {
	a, ok := allowed[req.Op]
	if !ok {
		return fail(KindInvalid, "op %q is not open, navigate, back, forward, reload, click, type, read, ax_tree, tabs or switch_tab", req.Op)
	}
	for name, bad := range map[string]bool{
		"url":       req.URL != "" && !a.url,
		"target":    req.Target != "" && !a.target,
		"text":      req.Text != "" && !a.text,
		"submit":    req.Submit && !a.submit,
		"tab":       req.Tab != 0 && !a.tab,
		"max_nodes": req.MaxNodes != 0 && !a.tree,
		"query":     req.Query != "" && !a.tree,
	} {
		if bad {
			return fail(KindInvalid, "%s does not take %s", req.Op, name)
		}
	}
	return nil
}

// Do carries out one request.
func (s *Service) Do(ctx context.Context, req protocol.BrowserReq) (protocol.BrowserRes, error) {
	if err := checkFields(req); err != nil {
		return protocol.BrowserRes{}, err
	}
	switch req.Op {
	case OpOpen:
		return s.open(ctx, req)
	case OpNavigate:
		return s.navigate(ctx, req)
	case OpBack, OpForward:
		delta := -1
		if req.Op == OpForward {
			delta = 1
		}
		if err := s.b.History(ctx, delta); err != nil {
			if errors.Is(err, ErrNoHistory) {
				return protocol.BrowserRes{}, fail(KindNotFound, "there is no page to go %s to", map[int]string{-1: "back", 1: "forward"}[delta])
			}
			return protocol.BrowserRes{}, s.mapErr(ctx, err, req.Op)
		}
		return s.state(ctx)
	case OpReload:
		if err := s.b.Reload(ctx); err != nil {
			return protocol.BrowserRes{}, s.mapErr(ctx, err, req.Op)
		}
		return s.state(ctx)
	case OpClick:
		return s.click(ctx, req)
	case OpType:
		return s.typeInto(ctx, req)
	case OpRead:
		return s.read(ctx)
	case OpAXTree:
		return s.axTree(ctx, req)
	case OpTabs:
		return s.tabs(ctx)
	}
	return s.switchTab(ctx, req)
}

// state is the answer to an operation that changes the page: where the active tab is now.
func (s *Service) state(ctx context.Context) (protocol.BrowserRes, error) {
	title, url, err := s.b.Summary(ctx)
	if err != nil {
		return protocol.BrowserRes{}, s.mapErr(ctx, err, "reading the page")
	}
	return protocol.BrowserRes{Title: textclean.Line(title, 256), URL: textclean.Line(url, 2048)}, nil
}

func (s *Service) open(ctx context.Context, req protocol.BrowserReq) (protocol.BrowserRes, error) {
	url := ""
	if req.URL != "" {
		var err error
		if url, err = CheckURL(req.URL); err != nil {
			return protocol.BrowserRes{}, err
		}
	}
	if err := s.b.Open(ctx, url); err != nil {
		return protocol.BrowserRes{}, s.mapErr(ctx, err, "opening the page")
	}
	return s.state(ctx)
}

func (s *Service) navigate(ctx context.Context, req protocol.BrowserReq) (protocol.BrowserRes, error) {
	url, err := CheckURL(req.URL)
	if err != nil {
		return protocol.BrowserRes{}, err
	}
	if err := s.b.Navigate(ctx, url); err != nil {
		return protocol.BrowserRes{}, s.mapErr(ctx, err, "navigating")
	}
	return s.state(ctx)
}

// aim is what a click or typing is aimed at: an element of the last tree, or a selector.
type aim struct {
	node     int64
	selector string
}

// resolve turns the request's target into a node or a selector. An id is looked up in the last tree and the element is
// asked about again: if the page was left or the element changed, it is stale.
func (s *Service) resolve(ctx context.Context, raw string) (aim, error) {
	if raw == "" {
		return aim{}, fail(KindInvalid, "target is required: an id from ax_tree (w12) or a CSS selector")
	}
	if !reNodeID.MatchString(raw) {
		if err := CheckSelector(raw); err != nil {
			return aim{}, err
		}
		return aim{selector: raw}, nil
	}
	s.mu.Lock()
	t := s.table
	s.mu.Unlock()
	if t == nil {
		return aim{}, fail(KindStale, "no accessibility tree of a page has been read yet; call ax_tree first")
	}
	e, ok := t.nodes[raw]
	if !ok {
		return aim{}, fail(KindNotFound, "the last tree has no element %s", raw)
	}
	tabs, err := s.b.Tabs(ctx)
	if err != nil {
		return aim{}, s.mapErr(ctx, err, "listing the tabs")
	}
	active := ""
	for _, tab := range tabs {
		if tab.Active {
			active = tab.ID
		}
	}
	if active != t.tab {
		return aim{}, fail(KindStale, "%s belongs to another tab than the active one; read the tree again", raw)
	}
	role, name, err := s.b.Describe(ctx, e.node)
	switch {
	case ctx.Err() != nil:
		return aim{}, ctx.Err()
	case errors.Is(err, ErrNodeGone):
		return aim{}, fail(KindStale, "%s (%s %q) is gone from the page; read the tree again", raw, e.role, e.name)
	case err != nil:
		return aim{}, s.mapErr(ctx, err, "checking the element")
	case role != e.role || name != e.name:
		return aim{}, fail(KindStale, "%s (%s %q) has changed; read the tree again", raw, e.role, e.name)
	}
	return aim{node: e.node}, nil
}

func (s *Service) click(ctx context.Context, req protocol.BrowserReq) (protocol.BrowserRes, error) {
	t, err := s.resolve(ctx, req.Target)
	if err != nil {
		return protocol.BrowserRes{}, err
	}
	if t.selector != "" {
		err = s.b.ClickSelector(ctx, t.selector)
	} else {
		err = s.b.ClickNode(ctx, t.node)
	}
	if err != nil {
		return protocol.BrowserRes{}, s.mapErr(ctx, err, "clicking")
	}
	return s.settled(ctx)
}

func (s *Service) typeInto(ctx context.Context, req protocol.BrowserReq) (protocol.BrowserRes, error) {
	if err := CheckText(req.Text); err != nil {
		return protocol.BrowserRes{}, err
	}
	t, err := s.resolve(ctx, req.Target)
	if err != nil {
		return protocol.BrowserRes{}, err
	}
	if t.selector != "" {
		err = s.b.TypeSelector(ctx, t.selector, req.Text, req.Submit)
	} else {
		err = s.b.TypeNode(ctx, t.node, req.Text, req.Submit)
	}
	if err != nil {
		return protocol.BrowserRes{}, s.mapErr(ctx, err, "typing")
	}
	return s.settled(ctx)
}

// settled waits a moment for what an action started (a navigation) and reports where the page is.
func (s *Service) settled(ctx context.Context) (protocol.BrowserRes, error) {
	if s.Settle > 0 {
		select {
		case <-time.After(s.Settle):
		case <-ctx.Done():
			return protocol.BrowserRes{}, ctx.Err()
		}
	}
	return s.state(ctx)
}

func (s *Service) read(ctx context.Context) (protocol.BrowserRes, error) {
	res, err := s.state(ctx)
	if err != nil {
		return res, err
	}
	text, err := s.b.VisibleText(ctx, MaxReadBytes)
	if err != nil {
		return protocol.BrowserRes{}, s.mapErr(ctx, err, "reading the page")
	}
	text = textclean.Block(text, MaxReadBytes)
	if len(text) > MaxReadBytes {
		text = cutBytes(text, MaxReadBytes)
		res.Truncated = true
	}
	res.Text = text
	return res, nil
}

// cutBytes cuts s to at most n bytes without splitting a character.
func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func (s *Service) tabs(ctx context.Context) (protocol.BrowserRes, error) {
	tabs, err := s.b.Tabs(ctx)
	if err != nil {
		return protocol.BrowserRes{}, s.mapErr(ctx, err, "listing the tabs")
	}
	res := protocol.BrowserRes{Tabs: make([]protocol.BrowserTab, 0, len(tabs))}
	for i, t := range tabs {
		res.Tabs = append(res.Tabs, protocol.BrowserTab{Index: i + 1, Title: textclean.Line(t.Title, 256), URL: textclean.Line(t.URL, 2048), Active: t.Active})
	}
	if len(tabs) == 0 {
		res.Note = "the browser has no tab open; use open"
	}
	return res, nil
}

func (s *Service) switchTab(ctx context.Context, req protocol.BrowserReq) (protocol.BrowserRes, error) {
	tabs, err := s.b.Tabs(ctx)
	if err != nil {
		return protocol.BrowserRes{}, s.mapErr(ctx, err, "listing the tabs")
	}
	if req.Tab < 1 || req.Tab > len(tabs) {
		return protocol.BrowserRes{}, fail(KindInvalid, "tab %d: the browser has %d tab(s), numbered from 1", req.Tab, len(tabs))
	}
	if err := s.b.Activate(ctx, tabs[req.Tab-1].ID); err != nil {
		return protocol.BrowserRes{}, s.mapErr(ctx, err, "switching tabs")
	}
	return s.state(ctx)
}

// clickable roles and the roles of fields, for the actions a node is listed with.
var (
	clickRoles = map[string]bool{
		"button": true, "link": true, "menuitem": true, "menuitemcheckbox": true, "menuitemradio": true, "tab": true,
		"checkbox": true, "radio": true, "switch": true, "option": true, "treeitem": true,
	}
	typeRoles = map[string]bool{"textbox": true, "searchbox": true, "combobox": true, "spinbutton": true, "password text": true}
)

func (s *Service) axTree(ctx context.Context, req protocol.BrowserReq) (protocol.BrowserRes, error) {
	if req.MaxNodes < 0 || req.MaxNodes > MaxMaxNodes {
		return protocol.BrowserRes{}, fail(KindInvalid, "max_nodes is 1 to %d", MaxMaxNodes)
	}
	max := req.MaxNodes
	if max == 0 {
		max = DefaultMaxNodes
	}
	if utf8.RuneCountInString(req.Query) > maxQueryRunes {
		return protocol.BrowserRes{}, fail(KindInvalid, "query is longer than %d characters", maxQueryRunes)
	}
	query := strings.ToLower(strings.TrimSpace(req.Query))

	snap, err := s.b.AXNodes(ctx)
	if err != nil {
		return protocol.BrowserRes{}, s.mapErr(ctx, err, "reading the page's accessibility tree")
	}
	var candidates []AXNode
	for _, n := range snap.Nodes {
		if query == "" || strings.Contains(strings.ToLower(n.Role+" "+n.Name+" "+n.Description+" "+n.Value), query) {
			candidates = append(candidates, n)
		}
	}

	// Elements are placed in batches until enough are found on the page; one that is not rendered has no box.
	var kept []protocol.A11yNode
	tbl := &table{tab: snap.Tab, nodes: map[string]entry{}}
	truncated := false
	for start := 0; start < len(candidates) && !truncated; start += boxBatch {
		batch := candidates[start:min(start+boxBatch, len(candidates))]
		ids := make([]int64, len(batch))
		for i, n := range batch {
			ids[i] = n.Node
		}
		boxes, err := s.b.Boxes(ctx, ids)
		if err != nil {
			return protocol.BrowserRes{}, s.mapErr(ctx, err, "placing the elements")
		}
		for _, n := range batch {
			b, ok := boxes[n.Node]
			if !ok || b.W <= 0 || b.H <= 0 {
				continue
			}
			if len(kept) == max {
				truncated = true
				break
			}
			id := "w" + strconv.Itoa(len(kept)+1)
			tbl.nodes[id] = entry{node: n.Node, role: n.Role, name: n.Name}
			kept = append(kept, protocol.A11yNode{ID: id, Role: n.Role, Name: n.Name, Description: n.Description, Value: n.Value,
				States: n.States, Bounds: protocol.Rect{b.X, b.Y, b.W, b.H}, Actions: actionsOf(n.Role)})
		}
	}

	s.mu.Lock()
	s.gen++
	gen := "g" + strconv.Itoa(s.gen)
	s.table = tbl
	s.mu.Unlock()

	res := protocol.BrowserRes{Generation: gen, Nodes: kept, Truncated: truncated}
	if len(kept) == 0 {
		res.Note = "the page shows nothing to read or operate (it may still be loading); try read, or a screenshot"
	}
	return res, nil
}

func actionsOf(role string) []string {
	switch {
	case clickRoles[role]:
		return []string{"click"}
	case typeRoles[role]:
		return []string{"type"}
	}
	return nil
}

// mapErr turns what the backend said into the errors the model hears about.
func (s *Service) mapErr(ctx context.Context, err error, what string) error {
	var e *Error
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.As(err, &e):
		return e
	case errors.Is(err, ErrNoBrowser):
		return fail(KindNotFound, "the browser is not open; open a page first")
	case errors.Is(err, ErrUnavailable):
		return fail(KindUnsupported, "%v", err)
	case errors.Is(err, ErrNodeGone):
		return fail(KindStale, "the element is gone from the page; read the tree again")
	case errors.Is(err, ErrNoMatch):
		return fail(KindNotFound, "no element matches the selector")
	case errors.Is(err, ErrBadSelector):
		return fail(KindInvalid, "the selector is not valid CSS")
	case errors.Is(err, context.DeadlineExceeded):
		return fail(KindTimeout, "%s took too long", what)
	}
	return fail(KindFailed, "%s failed: %s", what, textclean.Line(err.Error(), 300))
}
