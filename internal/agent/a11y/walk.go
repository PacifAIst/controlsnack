// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package a11y

import (
	"context"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PacifAIst/controlsnack/internal/agent/textclean"
)

// Defaults and limits of a walk (PLAN.md section 8).
const (
	DefaultMaxNodes = 150
	MaxMaxNodes     = 400
	DefaultMaxDepth = 30
	MaxMaxDepth     = 60
	DefaultBudget   = 1500 * time.Millisecond
	// defaultConcurrency is how many bus calls are in flight at once: enough to hide the latency of the bus, few enough
	// not to starve the applications being asked.
	defaultConcurrency = 8
	// exploreFactor bounds the work for one walk: at most this many elements per node asked for are looked at, and never
	// fewer than exploreFloor (the elements worth reporting are often below several levels of layout: a window of a GTK
	// application has six or seven before its first button, so a walk for five nodes must still get there). A list shows
	// the same: a file manager's detailed list has a cell for every column of every file, scrolled out of view or not,
	// and the name of a file is one level below its cell (D-69). The time budget, not this, is what normally ends a walk.
	exploreFactor = 8
	exploreFloor  = 1000

	maxNameRunes  = 300
	maxDescRunes  = 200
	maxValueRunes = 500
)

// Options bound and steer one walk.
type Options struct {
	MaxNodes    int
	MaxDepth    int
	Budget      time.Duration
	Query       string
	Concurrency int
}

func (o *Options) fill() {
	if o.MaxNodes <= 0 {
		o.MaxNodes = DefaultMaxNodes
	}
	o.MaxNodes = min(o.MaxNodes, MaxMaxNodes)
	if o.MaxDepth <= 0 {
		o.MaxDepth = DefaultMaxDepth
	}
	o.MaxDepth = min(o.MaxDepth, MaxMaxDepth)
	if o.Budget <= 0 {
		o.Budget = DefaultBudget
	}
	if o.Concurrency <= 0 {
		o.Concurrency = defaultConcurrency
	}
	o.Query = strings.ToLower(strings.TrimSpace(o.Query))
}

// Node is an element worth telling the model about.
type Node struct {
	Ref
	Role        string
	Name        string
	Description string
	Value       string
	States      []string
	X, Y, W, H  int
	Actions     []string
}

// Tree is what a walk found, in document order (a parent before its children, siblings in order).
type Tree struct {
	Nodes []Node
	// Truncated is set when the walk stopped early (node limit, depth limit or time budget) and more would have been
	// reported with more room.
	Truncated bool
}

// roles that are never worth reporting by themselves: layout and decoration. Their children are still visited.
var skipRoles = map[string]bool{
	"filler": true, "glass pane": true, "layered pane": true, "root pane": true, "scroll pane": true, "viewport": true,
	"redundant object": true, "separator": true, "split pane": true, "unknown": false, "tool tip": true,
}

// roles of elements a person can operate: reported even without a name.
var interactiveRoles = map[string]bool{
	"button": true, "toggle button": true, "check box": true, "check menu item": true, "radio button": true,
	"radio menu item": true, "menu item": true, "tear off menu item": true, "menu": true, "link": true, "entry": true,
	"password text": true, "text": true, "combo box": true, "spin button": true, "slider": true, "page tab": true,
	"terminal": true, "push button menu": true, "calendar": true, "date editor": true, "scroll bar": true, "autocomplete": true,
	"switch": true,
}

// roles of things a person can operate but that come in dozens (every cell of a table has four): reported only when the
// application gave them a name or a description, because a nameless one tells a model nothing.
var labelledRoles = map[string]bool{
	"list item": true, "tree item": true, "table cell": true, "icon": true, "desktop icon": true,
}

// roles that only hold other elements. They are reported when the application named them (a group box, a table); a
// description alone does not count, since toolkits put the tooltip of a button on the panel that wraps it as well.
var structuralRoles = map[string]bool{
	"panel": true, "tool bar": true, "menu bar": true, "popup menu": true, "table": true, "tree": true, "tree table": true,
	"list": true, "page tab list": true, "section": true, "grouping": true, "form": true, "header": true, "footer": true,
	"paragraph": true, "table row": true,
}

// operable reports whether a person can operate elements of this role (what the set-of-marks overlay boxes).
func operable(role string) bool { return interactiveRoles[role] || labelledRoles[role] }

// roles that offer actions worth asking for.
var actionableRoles = map[string]bool{
	"button": true, "toggle button": true, "check box": true, "check menu item": true, "radio button": true,
	"radio menu item": true, "menu item": true, "tear off menu item": true, "menu": true, "link": true, "combo box": true,
	"page tab": true, "list item": true, "tree item": true, "table cell": true, "icon": true, "desktop icon": true,
	"push button menu": true, "spin button": true, "entry": true, "switch": true,
}

// roles whose current text or number is worth reading. A password field is never read.
var textRoles = map[string]bool{"entry": true, "text": true, "terminal": true, "spin button": true, "document text": true, "document web": true}
var numberRoles = map[string]bool{"spin button": true, "slider": true, "progress bar": true, "level bar": true, "scroll bar": true}

type walker struct {
	bus Bus
	o   Options
	sem chan struct{}

	truncated atomic.Bool
}

// entry is an element the walk has reached.
type entry struct {
	ref   Ref
	depth int
	root  bool

	info      Info
	node      *Node // set when the element is reported
	childRefs []Ref
	kids      []*entry
}

// Walk reads the elements below the roots, within the limits of the options. It goes level by level, so what a
// limit leaves out is always the deepest and last elements, and the same window gives the same answer every time;
// the elements come back in document order (a parent before its children, siblings in order). Everything the bus says
// about an element is cleaned before it is returned. When the context ends or the budget is spent, what was found
// so far comes back with Truncated set; that is not an error.
func Walk(ctx context.Context, bus Bus, roots []Ref, o Options) (Tree, error) {
	o.fill()
	w := &walker{bus: bus, o: o, sem: make(chan struct{}, o.Concurrency)}
	ctx, cancel := context.WithTimeout(ctx, o.Budget)
	defer cancel()

	seen := map[Ref]bool{}
	var level, top []*entry
	for _, r := range roots {
		if !seen[r] {
			seen[r] = true
			level = append(level, &entry{ref: r, root: true})
		}
	}
	top = slices.Clone(level)

	limit := max(o.MaxNodes*exploreFactor, exploreFloor)
	explored := 0
	for len(level) > 0 {
		if ctx.Err() != nil {
			break
		}
		if room := limit - explored; len(level) > room {
			level = level[:max(room, 0)]
			w.truncated.Store(true)
		}
		explored += len(level)
		var wg sync.WaitGroup
		for _, e := range level {
			wg.Add(1)
			go func() {
				defer wg.Done()
				w.expand(ctx, e)
			}()
		}
		wg.Wait()

		// The next level, in document order. Links are made here, one goroutine, so the same element reached twice
		// (or an ancestor listed as a child) is taken at its first place only.
		var next []*entry
		for _, e := range level {
			for _, r := range e.childRefs {
				if !seen[r] {
					seen[r] = true
					e.kids = append(e.kids, &entry{ref: r, depth: e.depth + 1})
				}
			}
			next = append(next, e.kids...)
		}
		level = next
	}
	if ctx.Err() != nil {
		w.truncated.Store(true)
	}

	var nodes []Node
	for _, e := range top {
		w.collect(e, &nodes)
	}
	t := Tree{Nodes: nodes, Truncated: w.truncated.Load()}
	if len(t.Nodes) > o.MaxNodes {
		t.Nodes, t.Truncated = t.Nodes[:o.MaxNodes], true
	}
	return t, nil
}

// collect appends the reported elements of a subtree in document order, those that match the query.
func (w *walker) collect(e *entry, out *[]Node) {
	if len(*out) > w.o.MaxNodes {
		return
	}
	if e.node != nil && w.matches(*e.node) {
		*out = append(*out, *e.node)
	}
	for _, k := range e.kids {
		w.collect(k, out)
	}
}

// call runs one bus operation under the concurrency limit.
func (w *walker) call(ctx context.Context, f func()) bool {
	select {
	case w.sem <- struct{}{}:
	case <-ctx.Done():
		return false
	}
	defer func() { <-w.sem }()
	f()
	return true
}

// expand reads one element: what it is, whether it is worth reporting, and what is below it. The elements reached
// from the roots are reported and entered even if they do not look like anything.
func (w *walker) expand(ctx context.Context, e *entry) {
	var err error
	if !w.call(ctx, func() { e.info, err = w.bus.Info(ctx, e.ref) }) || err != nil {
		return // gone, or the budget ended
	}
	if e.info.State.Has(StateDefunct) || (!e.root && !e.info.State.Showing()) {
		return
	}
	role := RoleName(e.info.Role)

	var wg sync.WaitGroup
	if e.root || w.worth(role, e.info) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if n, ok := w.describe(ctx, e.ref, role, e.info); ok {
				e.node = &n
			}
		}()
	}
	if e.info.ChildCount > 0 {
		if e.depth >= w.o.MaxDepth {
			w.truncated.Store(true)
		} else {
			wg.Add(1)
			go func() {
				defer wg.Done()
				w.call(ctx, func() {
					if kids, err := w.bus.Children(ctx, e.ref); err == nil {
						e.childRefs = kids
					}
				})
			}()
		}
	}
	wg.Wait()
}

// worth decides whether an element is reported: it must be something a person can operate or read.
func (w *walker) worth(role string, info Info) bool {
	switch {
	case skipRoles[role]:
		return false
	case labelledRoles[role]:
		return info.Name != "" || info.Description != ""
	case interactiveRoles[role]:
		return true
	case structuralRoles[role]:
		return info.Name != ""
	}
	return info.Name != "" || info.Description != ""
}

// describe asks for the rest: where the element is, what it can do and what it holds, all at once.
func (w *walker) describe(ctx context.Context, ref Ref, role string, info Info) (Node, bool) {
	n := Node{Ref: ref, Role: role, Name: textclean.Line(info.Name, maxNameRunes), Description: textclean.Line(info.Description, maxDescRunes),
		States: info.State.Names()}
	var wg sync.WaitGroup
	run := func(f func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.call(ctx, f)
		}()
	}
	run(func() {
		if x, y, ww, h, err := w.bus.Extents(ctx, ref); err == nil {
			n.X, n.Y, n.W, n.H = x, y, ww, h
		}
	})
	if actionableRoles[role] {
		run(func() {
			if acts, err := w.bus.Actions(ctx, ref); err == nil {
				for _, a := range acts {
					if a = textclean.Line(a, 40); a != "" {
						n.Actions = append(n.Actions, a)
					}
				}
			}
		})
	}
	if textRoles[role] && role != "password text" {
		run(func() {
			if s, err := w.bus.Text(ctx, ref, maxValueRunes*2); err == nil {
				n.Value = textclean.Block(s, maxValueRunes)
			}
		})
	} else if numberRoles[role] {
		run(func() {
			if f, err := w.bus.Number(ctx, ref); err == nil {
				n.Value = formatNumber(f)
			}
		})
	}
	wg.Wait()
	if ctx.Err() != nil {
		return n, false
	}
	// An element with no area cannot be seen or clicked; it is noise whatever it is called.
	if n.W <= 0 || n.H <= 0 {
		if !isRootLike(role) {
			return n, false
		}
	}
	// A cell of a list that is scrolled out of view still exists, and GTK puts it at the smallest number there is.
	if n.X == math.MinInt32 || n.Y == math.MinInt32 {
		return n, false
	}
	return n, true
}

// isRootLike names the elements that are reported even without an area: an application's own entry.
func isRootLike(role string) bool { return role == "application" }

// matches applies the query: it must occur in the role, name, description or value.
func (w *walker) matches(n Node) bool {
	if w.o.Query == "" {
		return true
	}
	hay := strings.ToLower(n.Role + " " + n.Name + " " + n.Description + " " + n.Value)
	return strings.Contains(hay, w.o.Query)
}

// formatNumber writes a value without trailing zeros: 50, 0.25.
func formatNumber(f float64) string {
	s := strconv.FormatFloat(f, 'f', 3, 64)
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}
