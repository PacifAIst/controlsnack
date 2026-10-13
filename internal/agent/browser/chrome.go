// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package browser

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// Timeouts of what the backend asks the browser to do.
const (
	startTimeout = 3 * time.Minute
	// navigateTimeout bounds the wait for a page to load. A page that never finishes (a server that never answers) must
	// not hold the one browser worker for ever; the host's own timeout is shorter in practice.
	navigateTimeout = 90 * time.Second
	actionTimeout   = 20 * time.Second
	boxConcurrency  = 8
	// modCtrl is the bit of the Control key in the modifiers of an input event.
	modCtrl = 2
)

// Chromium is a Backend that starts the guest's Chromium and drives it over the DevTools protocol, through a pipe (no
// debugging port is opened). It runs one browser, in a window of the desktop, for the life of the agent.
type Chromium struct {
	// Path is the browser program (/usr/bin/chromium).
	Path string
	// UserDataDir is the browser's profile.
	UserDataDir string
	// Proxy, when set, is the proxy every request goes through (http://127.0.0.1:3128).
	Proxy string
	// Options are added to the browser's command line options, after the ones above (for tests).
	Options []chromedp.ExecAllocatorOption

	mu     sync.Mutex
	alloc  context.CancelFunc
	root   context.Context
	cancel context.CancelFunc
	tabs   map[string]*tab
	order  []string
	seen   map[string]bool
	active string
}

// tab is a page the backend has a context for.
type tab struct {
	id     string
	ctx    context.Context
	cancel context.CancelFunc
}

// NewChromium returns a backend for the browser at path.
func NewChromium(path, userDataDir, proxy string) *Chromium {
	return &Chromium{Path: path, UserDataDir: userDataDir, Proxy: proxy}
}

func (c *Chromium) options() []chromedp.ExecAllocatorOption {
	opts := []chromedp.ExecAllocatorOption{
		chromedp.ExecPath(c.Path),
		chromedp.UserDataDir(c.UserDataDir),
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.VisibleWindow,
		// Pages expose their accessibility tree to the desktop's accessibility service too.
		chromedp.Flag("force-renderer-accessibility", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("password-store", "basic"),
		chromedp.Flag("disable-features", "Translate,MediaRouter"),
		chromedp.Flag("disable-session-crashed-bubble", true),
		chromedp.Flag("hide-crash-restore-bubble", true),
		chromedp.Flag("disable-infobars", true),
	}
	if c.Proxy != "" {
		opts = append(opts, chromedp.Flag("proxy-server", c.Proxy))
	}
	return append(opts, c.Options...)
}

// start launches the browser. The mutex is held.
func (c *Chromium) start(reqCtx context.Context) error {
	if _, err := os.Stat(c.Path); err != nil {
		return fmt.Errorf("%w: %s", ErrUnavailable, c.Path)
	}
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), c.options()...)
	root, cancel := chromedp.NewContext(allocCtx)
	// The first Run starts the browser, and the first tab is the one it opens.
	id, err := firstRun(reqCtx, root, cancel, startTimeout, func(ctx context.Context, t *chromedp.Target) (string, error) {
		return string(t.TargetID), nil
	})
	if err != nil {
		cancel()
		allocCancel()
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	c.alloc, c.root, c.cancel = allocCancel, root, cancel
	c.tabs = map[string]*tab{id: {id: id, ctx: root, cancel: func() {}}}
	c.order = []string{id}
	c.seen = map[string]bool{id: true}
	c.active = id
	return nil
}

// firstRun runs the first action of a chromedp context, which starts the browser or opens the tab. That Run must get the
// context itself and not one with a timeout (the timeout would stop the browser), so the time limit and the request's
// cancellation cancel the context instead.
func firstRun[T any](reqCtx, cctx context.Context, cancel context.CancelFunc, timeout time.Duration, a chromedp.Action[T]) (T, error) {
	timer := time.AfterFunc(timeout, cancel)
	defer timer.Stop()
	stop := context.AfterFunc(reqCtx, cancel)
	defer stop()
	return chromedp.Run(cctx, a)
}

// running reports whether the browser is up; a browser that exited is forgotten. The mutex is held.
func (c *Chromium) running() bool {
	if c.root == nil {
		return false
	}
	if c.root.Err() != nil {
		c.forget()
		return false
	}
	return true
}

func (c *Chromium) forget() {
	if c.cancel != nil {
		c.cancel()
	}
	if c.alloc != nil {
		c.alloc()
	}
	c.root, c.cancel, c.alloc, c.tabs, c.order, c.seen, c.active = nil, nil, nil, nil, nil, nil, ""
}

// Close ends the browser.
func (c *Chromium) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.root == nil {
		return nil
	}
	cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = chromedp.Cancel(cctx) // not needed for the process: forget() kills it
	c.forget()
	return nil
}

// refresh reads the browser's pages and brings the tab list up to date: tabs that closed are dropped, and a page that
// appeared since the last look (a link that opens a window, a script that does) is taken to be in front. The mutex is
// held.
func (c *Chromium) refresh(ctx context.Context) ([]*target.Info, error) {
	rctx, cancel := context.WithTimeout(c.root, actionTimeout)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	infos, err := chromedp.Targets(rctx)
	if err != nil {
		if c.root.Err() != nil {
			c.forget()
			return nil, ErrNoBrowser
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	var pages []*target.Info
	live := map[string]bool{}
	for _, in := range infos {
		if in.Type == "page" {
			pages = append(pages, in)
			live[string(in.TargetID)] = true
		}
	}
	for id, t := range c.tabs {
		if !live[id] {
			t.cancel()
			delete(c.tabs, id)
			c.order = removeString(c.order, id)
		}
	}
	for _, in := range pages {
		id := string(in.TargetID)
		if !c.seen[id] {
			c.seen[id] = true
			c.order = append(c.order, id)
			c.active = id
		}
	}
	if !live[c.active] && len(c.order) > 0 {
		c.active = c.order[len(c.order)-1]
	}
	// Pages in our order, so a tab keeps its number.
	byID := map[string]*target.Info{}
	for _, in := range pages {
		byID[string(in.TargetID)] = in
	}
	sorted := make([]*target.Info, 0, len(pages))
	for _, id := range c.order {
		if in, ok := byID[id]; ok {
			sorted = append(sorted, in)
		}
	}
	return sorted, ctx.Err()
}

func removeString(s []string, v string) []string {
	out := s[:0]
	for _, x := range s {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}

// Tabs lists the pages.
func (c *Chromium) Tabs(ctx context.Context) ([]Tab, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.running() {
		return nil, nil
	}
	pages, err := c.refresh(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Tab, len(pages))
	for i, p := range pages {
		out[i] = Tab{ID: string(p.TargetID), Title: p.Title, URL: p.URL, Active: string(p.TargetID) == c.active}
	}
	return out, nil
}

// tabContext returns the context of the active tab, attaching to the page if it is not one the backend made.
func (c *Chromium) tabContext(ctx context.Context) (*tab, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.running() {
		return nil, ErrNoBrowser
	}
	if _, err := c.refresh(ctx); err != nil {
		return nil, err
	}
	if c.active == "" {
		return nil, ErrNoBrowser
	}
	return c.attach(c.active)
}

// attach finds or makes the context of a page. The mutex is held.
func (c *Chromium) attach(id string) (*tab, error) {
	if t, ok := c.tabs[id]; ok {
		return t, nil
	}
	tctx, cancel := chromedp.NewContext(c.root, chromedp.WithTargetID(target.ID(id)), chromedp.WithDetachOnCancel())
	if _, err := chromedp.Run(tctx, func(context.Context, *chromedp.Target) (struct{}, error) { return struct{}{}, nil }); err != nil {
		cancel()
		return nil, err
	}
	t := &tab{id: id, ctx: tctx, cancel: cancel}
	c.tabs[id] = t
	return t, nil
}

// run runs f with a context that ends when the tab's does, when the request's does, or after the timeout.
func (c *Chromium) run(ctx context.Context, timeout time.Duration, f func(ctx context.Context, t *chromedp.Target) error) error {
	t, err := c.tabContext(ctx)
	if err != nil {
		return err
	}
	return c.runIn(ctx, t, timeout, f)
}

func (c *Chromium) runIn(ctx context.Context, t *tab, timeout time.Duration, f func(ctx context.Context, t *chromedp.Target) error) error {
	opCtx, cancel := context.WithTimeout(t.ctx, timeout)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	_, err := chromedp.Run(opCtx, func(ctx context.Context, tg *chromedp.Target) (struct{}, error) { return struct{}{}, f(ctx, tg) })
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil && t.ctx.Err() != nil {
		c.mu.Lock()
		c.running() // forgets a browser that is gone
		c.mu.Unlock()
		return ErrNoBrowser
	}
	return err
}

// Open opens a page in a new tab, starting the browser first if it is not running.
func (c *Chromium) Open(ctx context.Context, url string) error {
	c.mu.Lock()
	if !c.running() {
		if err := c.start(ctx); err != nil {
			c.mu.Unlock()
			return err
		}
		first := c.tabs[c.active]
		c.mu.Unlock()
		if url == "" {
			return nil
		}
		return c.runIn(ctx, first, navigateTimeout, navigateTo(url))
	}
	tctx, cancel := chromedp.NewContext(c.root, chromedp.WithNewWindow(false))
	c.mu.Unlock()
	id, err := firstRun(ctx, tctx, cancel, actionTimeout, func(ctx context.Context, t *chromedp.Target) (string, error) { return string(t.TargetID), nil })
	if err != nil {
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	c.mu.Lock()
	t := &tab{id: id, ctx: tctx, cancel: cancel}
	c.tabs[id] = t
	if !c.seen[id] {
		c.seen[id] = true
		c.order = append(c.order, id)
	}
	c.active = id
	c.mu.Unlock()
	if url == "" {
		return nil
	}
	return c.runIn(ctx, t, navigateTimeout, navigateTo(url))
}

func navigateTo(url string) func(ctx context.Context, t *chromedp.Target) error {
	return func(ctx context.Context, t *chromedp.Target) error {
		_, err := chromedp.Navigate(url)(ctx, t)
		return err
	}
}

// Activate brings a tab to the front.
func (c *Chromium) Activate(ctx context.Context, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.running() {
		return ErrNoBrowser
	}
	if _, err := c.refresh(ctx); err != nil {
		return err
	}
	if !c.seen[id] {
		return ErrNoBrowser
	}
	if _, err := cdp.Call(ctx, chromedp.FromContext(c.root).Browser, target.ActivateTarget, target.ActivateTargetParams{TargetID: target.ID(id)}); err != nil {
		return err
	}
	c.active = id
	return nil
}

// Navigate loads an address in the active tab.
func (c *Chromium) Navigate(ctx context.Context, url string) error {
	return c.run(ctx, navigateTimeout, navigateTo(url))
}

// History goes back or forward in the active tab's history.
func (c *Chromium) History(ctx context.Context, delta int) error {
	return c.run(ctx, navigateTimeout, func(ctx context.Context, t *chromedp.Target) error {
		h, err := chromedp.NavigationEntries()(ctx, t)
		if err != nil {
			return err
		}
		i := int(h.CurrentIndex) + delta
		if i < 0 || i >= len(h.Entries) {
			return ErrNoHistory
		}
		_, err = chromedp.NavigateToHistoryEntry(h.Entries[i].ID)(ctx, t)
		return err
	})
}

// Reload loads the active tab's page again.
func (c *Chromium) Reload(ctx context.Context) error {
	return c.run(ctx, navigateTimeout, func(ctx context.Context, t *chromedp.Target) error {
		_, err := chromedp.Reload()(ctx, t)
		return err
	})
}

// Summary is the title and address of the active tab's page.
func (c *Chromium) Summary(ctx context.Context) (title, url string, err error) {
	err = c.run(ctx, actionTimeout, func(ctx context.Context, t *chromedp.Target) error {
		var e error
		if title, e = chromedp.Title()(ctx, t); e != nil {
			return e
		}
		url, e = chromedp.Location()(ctx, t)
		return e
	})
	return title, url, err
}

// VisibleText is the text of the page a person would read.
func (c *Chromium) VisibleText(ctx context.Context, max int) (text string, err error) {
	err = c.run(ctx, actionTimeout, func(ctx context.Context, t *chromedp.Target) error {
		var e error
		text, e = chromedp.Evaluate[string](fmt.Sprintf(`(document.body ? document.body.innerText : "").slice(0, %d)`, max*2))(ctx, t)
		return e
	})
	return text, err
}

// passwordFields finds the password fields of the page (of its main frame) and returns their backend node ids.
func passwordFields(ctx context.Context, t *chromedp.Target) (map[int64]bool, error) {
	out := map[int64]bool{}
	doc, err := cdp.Call(ctx, t, dom.GetDocument, dom.GetDocumentParams{Depth: new(int64(0))})
	if err != nil || doc.Root == nil {
		return out, err
	}
	q, err := cdp.Call(ctx, t, dom.QuerySelectorAll, dom.QuerySelectorAllParams{NodeID: doc.Root.NodeID, Selector: "input[type=password i]"})
	if err != nil {
		return out, err
	}
	for _, id := range q.NodeIDs {
		d, err := cdp.Call(ctx, t, dom.DescribeNode, dom.DescribeNodeParams{NodeID: id})
		if err == nil && d.Node != nil {
			out[int64(d.Node.BackendNodeID)] = true
		}
	}
	return out, nil
}

// AXNodes reads the accessibility tree of the active tab.
func (c *Chromium) AXNodes(ctx context.Context) (snap Snapshot, err error) {
	tb, err := c.tabContext(ctx)
	if err != nil {
		return snap, err
	}
	snap.Tab = tb.id
	err = c.runIn(ctx, tb, actionTimeout, func(ctx context.Context, t *chromedp.Target) error {
		if _, e := cdp.Call(ctx, t, accessibility.Enable, cdp.Empty{}); e != nil {
			return e
		}
		res, e := cdp.Call(ctx, t, accessibility.GetFullAXTree, accessibility.GetFullAXTreeParams{})
		if e != nil {
			return e
		}
		passwords, e := passwordFields(ctx, t)
		if e != nil {
			return e
		}
		snap.Nodes = MapAX(res.Nodes, passwords)
		return nil
	})
	return snap, err
}

// viewportOrigin is where the page's viewport starts on the screen, and the pixel ratio. It is an approximation: the
// window's outer size minus the page's inner size is shared out as equal borders at the sides and the rest on top
// (the tab strip and the toolbar), which is how Chromium lays out its window.
func viewportOrigin(ctx context.Context, t *chromedp.Target) (ox, oy, ratio float64, err error) {
	v, err := chromedp.Evaluate[[]float64](`[window.screenX, window.screenY, window.outerWidth, window.outerHeight, window.innerWidth, window.innerHeight, window.devicePixelRatio]`)(ctx, t)
	if err != nil {
		return 0, 0, 1, fmt.Errorf("reading the window: %w", err)
	}
	if len(v) != 7 {
		return 0, 0, 1, errors.New("reading the window: the page gave an unexpected answer")
	}
	side := (v[2] - v[4]) / 2
	if side < 0 {
		side = 0
	}
	ratio = v[6]
	if ratio <= 0 {
		ratio = 1
	}
	return v[0] + side, v[1] + (v[3] - v[5]) - side, ratio, nil
}

// quadBounds is the rectangle around a quad of eight numbers (four corners).
func quadBounds(q dom.Quad) (x0, y0, x1, y1 float64, ok bool) {
	if len(q) != 8 {
		return 0, 0, 0, 0, false
	}
	x0, y0, x1, y1 = math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for i := 0; i < 8; i += 2 {
		x0, x1 = math.Min(x0, q[i]), math.Max(x1, q[i])
		y0, y1 = math.Min(y0, q[i+1]), math.Max(y1, q[i+1])
	}
	return x0, y0, x1, y1, true
}

// Boxes places elements on the screen.
func (c *Chromium) Boxes(ctx context.Context, nodes []int64) (map[int64]Box, error) {
	out := map[int64]Box{}
	var mu sync.Mutex
	err := c.run(ctx, actionTimeout*2, func(ctx context.Context, t *chromedp.Target) error {
		ox, oy, ratio, err := viewportOrigin(ctx, t)
		if err != nil {
			return err
		}
		sem := make(chan struct{}, boxConcurrency)
		var wg sync.WaitGroup
		for _, n := range nodes {
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				m, err := cdp.Call(ctx, t, dom.GetBoxModel, dom.GetBoxModelParams{BackendNodeID: cdp.BackendNodeID(n)})
				if err != nil || m.Model == nil {
					return // not rendered, or gone
				}
				x0, y0, x1, y1, ok := quadBounds(m.Model.Border)
				if !ok {
					return
				}
				b := Box{X: int(math.Round(ox + x0*ratio)), Y: int(math.Round(oy + y0*ratio)), W: int(math.Round((x1 - x0) * ratio)), H: int(math.Round((y1 - y0) * ratio))}
				mu.Lock()
				out[n] = b
				mu.Unlock()
			}()
		}
		wg.Wait()
		return ctx.Err()
	})
	return out, err
}

// isGone says whether a protocol error means the element is not there any more.
func isGone(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "no node with given id") || strings.Contains(s, "could not find node") ||
		strings.Contains(s, "node with given id") || strings.Contains(s, "does not belong to the document") ||
		strings.Contains(s, "node is detached") || strings.Contains(s, "cannot find context") || strings.Contains(s, "frame is detached")
}

// Describe reads an element's role and name now.
func (c *Chromium) Describe(ctx context.Context, node int64) (role, name string, err error) {
	err = c.run(ctx, actionTimeout, func(ctx context.Context, t *chromedp.Target) error {
		res, e := cdp.Call(ctx, t, accessibility.GetPartialAXTree, accessibility.GetPartialAXTreeParams{BackendNodeID: cdp.BackendNodeID(node)})
		if e != nil {
			if isGone(e) {
				return ErrNodeGone
			}
			return e
		}
		if len(res.Nodes) == 0 {
			return ErrNodeGone
		}
		passwords, e := passwordFields(ctx, t)
		if e != nil {
			return e
		}
		mapped := MapAX(res.Nodes[:1], map[int64]bool{node: passwords[node]})
		if len(mapped) == 1 {
			role, name = mapped[0].Role, mapped[0].Name
			return nil
		}
		// A role the tree does not report (it was reported when it was read, so it has changed).
		role = strings.ToLower(jsonString(res.Nodes[0].Role))
		name = jsonString(res.Nodes[0].Name)
		return nil
	})
	return role, name, err
}

// center is the middle of an element in viewport coordinates, after scrolling it into view.
func center(ctx context.Context, t *chromedp.Target, node int64) (x, y float64, err error) {
	id := cdp.BackendNodeID(node)
	if _, err = cdp.Call(ctx, t, dom.ScrollIntoViewIfNeeded, dom.ScrollIntoViewIfNeededParams{BackendNodeID: id}); err != nil {
		if isGone(err) {
			return 0, 0, ErrNodeGone
		}
		return 0, 0, err
	}
	m, err := cdp.Call(ctx, t, dom.GetBoxModel, dom.GetBoxModelParams{BackendNodeID: id})
	if err != nil || m.Model == nil {
		if isGone(err) {
			return 0, 0, ErrNodeGone
		}
		return 0, 0, fmt.Errorf("the element has no place on the page (it is not shown)")
	}
	x0, y0, x1, y1, ok := quadBounds(m.Model.Border)
	if !ok || x1 <= x0 || y1 <= y0 {
		return 0, 0, fmt.Errorf("the element has no area on the page")
	}
	return (x0 + x1) / 2, (y0 + y1) / 2, nil
}

func clickAt(ctx context.Context, t *chromedp.Target, x, y float64) error {
	for _, ev := range []input.DispatchMouseEventParams{
		{Type: input.DispatchMouseEventTypeMouseMoved, X: x, Y: y},
		{Type: input.DispatchMouseEventTypeMousePressed, X: x, Y: y, Button: input.MouseButtonLeft, Buttons: 1, ClickCount: 1},
		{Type: input.DispatchMouseEventTypeMouseReleased, X: x, Y: y, Button: input.MouseButtonLeft, ClickCount: 1},
	} {
		if _, err := cdp.Call(ctx, t, input.DispatchMouseEvent, ev); err != nil {
			return err
		}
	}
	return nil
}

// ClickNode clicks the middle of an element, scrolling it into view first.
func (c *Chromium) ClickNode(ctx context.Context, node int64) error {
	return c.run(ctx, actionTimeout, func(ctx context.Context, t *chromedp.Target) error {
		x, y, err := center(ctx, t, node)
		if err != nil {
			return err
		}
		return clickAt(ctx, t, x, y)
	})
}

// resolveSelector finds the first element that matches a selector and returns its backend node id.
func resolveSelector(ctx context.Context, t *chromedp.Target, sel string) (int64, error) {
	doc, err := cdp.Call(ctx, t, dom.GetDocument, dom.GetDocumentParams{Depth: new(int64(0))})
	if err != nil {
		return 0, fmt.Errorf("reading the page: %w", err)
	}
	if doc.Root == nil {
		return 0, errors.New("reading the page: it has no document")
	}
	q, err := cdp.Call(ctx, t, dom.QuerySelector, dom.QuerySelectorParams{NodeID: doc.Root.NodeID, Selector: sel})
	if err != nil {
		if strings.Contains(err.Error(), "DOM Error while querying") || strings.Contains(strings.ToLower(err.Error()), "not a valid selector") {
			return 0, ErrBadSelector
		}
		return 0, err
	}
	if q.NodeID == 0 {
		return 0, ErrNoMatch
	}
	d, err := cdp.Call(ctx, t, dom.DescribeNode, dom.DescribeNodeParams{NodeID: q.NodeID})
	if err != nil || d.Node == nil {
		return 0, ErrNoMatch
	}
	return int64(d.Node.BackendNodeID), nil
}

// ClickSelector clicks the first element that matches a selector.
func (c *Chromium) ClickSelector(ctx context.Context, sel string) error {
	return c.run(ctx, actionTimeout, func(ctx context.Context, t *chromedp.Target) error {
		node, err := resolveSelector(ctx, t, sel)
		if err != nil {
			return err
		}
		x, y, err := center(ctx, t, node)
		if err != nil {
			return err
		}
		return clickAt(ctx, t, x, y)
	})
}

func key(ctx context.Context, t *chromedp.Target, down input.DispatchKeyEventParams, up input.DispatchKeyEventParams) error {
	if _, err := cdp.Call(ctx, t, input.DispatchKeyEvent, down); err != nil {
		return err
	}
	_, err := cdp.Call(ctx, t, input.DispatchKeyEvent, up)
	return err
}

func typeInto(ctx context.Context, t *chromedp.Target, node int64, text string, submit bool) error {
	if _, err := cdp.Call(ctx, t, dom.ScrollIntoViewIfNeeded, dom.ScrollIntoViewIfNeededParams{BackendNodeID: cdp.BackendNodeID(node)}); err != nil {
		if isGone(err) {
			return ErrNodeGone
		}
		return err
	}
	if _, err := cdp.Call(ctx, t, dom.Focus, dom.FocusParams{BackendNodeID: cdp.BackendNodeID(node)}); err != nil {
		if isGone(err) {
			return ErrNodeGone
		}
		return err
	}
	// What is in the field is replaced, as when a person selects it all and types.
	if err := key(ctx, t,
		input.DispatchKeyEventParams{Type: input.DispatchKeyEventTypeRawKeyDown, Key: "a", Code: "KeyA", WindowsVirtualKeyCode: 65, NativeVirtualKeyCode: 65, Modifiers: modCtrl, Commands: []string{"selectAll"}},
		input.DispatchKeyEventParams{Type: input.DispatchKeyEventTypeKeyUp, Key: "a", Code: "KeyA", WindowsVirtualKeyCode: 65, NativeVirtualKeyCode: 65, Modifiers: modCtrl}); err != nil {
		return err
	}
	if text == "" {
		if err := key(ctx, t,
			input.DispatchKeyEventParams{Type: input.DispatchKeyEventTypeRawKeyDown, Key: "Backspace", Code: "Backspace", WindowsVirtualKeyCode: 8, NativeVirtualKeyCode: 8, Commands: []string{"deleteBackward"}},
			input.DispatchKeyEventParams{Type: input.DispatchKeyEventTypeKeyUp, Key: "Backspace", Code: "Backspace", WindowsVirtualKeyCode: 8, NativeVirtualKeyCode: 8}); err != nil {
			return err
		}
	} else if _, err := cdp.Call(ctx, t, input.InsertText, input.InsertTextParams{Text: text}); err != nil {
		return err
	}
	if submit {
		return key(ctx, t,
			input.DispatchKeyEventParams{Type: input.DispatchKeyEventTypeKeyDown, Key: "Enter", Code: "Enter", Text: "\r", WindowsVirtualKeyCode: 13, NativeVirtualKeyCode: 13},
			input.DispatchKeyEventParams{Type: input.DispatchKeyEventTypeKeyUp, Key: "Enter", Code: "Enter", WindowsVirtualKeyCode: 13, NativeVirtualKeyCode: 13})
	}
	return nil
}

// TypeNode puts text in an element, replacing what is there.
func (c *Chromium) TypeNode(ctx context.Context, node int64, text string, submit bool) error {
	return c.run(ctx, actionTimeout, func(ctx context.Context, t *chromedp.Target) error {
		return typeInto(ctx, t, node, text, submit)
	})
}

// TypeSelector puts text in the first element that matches a selector.
func (c *Chromium) TypeSelector(ctx context.Context, sel, text string, submit bool) error {
	return c.run(ctx, actionTimeout, func(ctx context.Context, t *chromedp.Target) error {
		node, err := resolveSelector(ctx, t, sel)
		if err != nil {
			return err
		}
		return typeInto(ctx, t, node, text, submit)
	})
}

var _ Backend = (*Chromium)(nil)
