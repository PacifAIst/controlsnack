// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package a11y

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/PacifAIst/controlsnack/internal/agent/textclean"
)

// Scopes of a tree request.
const (
	ScopeActiveWindow = "active_window"
	ScopeDesktop      = "desktop"
	ScopeBrowser      = "browser"
)

// Operations of an action request.
const (
	OpClick   = "click"
	OpFocus   = "focus"
	OpSetText = "set_text"
)

// Limits of the service.
const (
	// keptGenerations is how many generations of ids stay valid: the latest, and a few before it so a batch of actions
	// planned from one observation is not refused because a tree was read in between.
	keptGenerations = 4
	maxQueryRunes   = 100
	// MaxSetTextRunes is the longest text set into a field by one request.
	MaxSetTextRunes = 4000
	// defaultActionTimeout is how long to wait for an element to finish an action. A button that opens a dialog with its own
	// event loop answers only when the dialog closes; the action was done, and waiting for it would stall the agent.
	defaultActionTimeout = 2 * time.Second
)

// Kind classifies an Error; the agent maps each to a protocol error code.
type Kind int

// Kinds of Error.
const (
	KindInvalid     Kind = iota // the request is wrong (bad scope, operation, text)
	KindNotFound                // no such element in a generation that exists
	KindStale                   // the generation is gone, or the element has gone or changed since it was shown
	KindUnsupported             // the element, or the whole desktop, cannot do this
	KindFailed                  // the element did not do it
)

// Error is a failure the model can be told about.
type Error struct {
	Kind Kind
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

func fail(k Kind, format string, args ...any) error {
	return &Error{Kind: k, Msg: fmt.Sprintf(format, args...)}
}

// Window is what the service needs to know about the window that has the focus.
type Window struct {
	PID   int
	Title string
}

// Desktop reports the window that has the focus; nil, nil when there is none.
type Desktop interface {
	ActiveWindow(ctx context.Context) (*Window, error)
}

// Driver does what the accessibility bus cannot: it clicks and types like a person, when an element has no action of
// its own or does not accept text directly.
type Driver interface {
	Click(ctx context.Context, x, y int) error
	Keys(ctx context.Context, combo string) error
	Type(ctx context.Context, text string) error
}

// Entry is a reported element with the id it has in its generation.
type Entry struct {
	ID string
	Node
}

// Result is one tree: a generation, its elements, and why it is short if it is.
type Result struct {
	Generation string
	Entries    []Entry
	Truncated  bool
	Note       string
}

// Service reads the accessibility tree and acts on its elements. Ids are valid for the generation they came with.
type Service struct {
	bus     Bus
	desktop Desktop
	driver  Driver

	// actionTimeout is how long an action may take to answer (defaultActionTimeout; tests make it short).
	actionTimeout time.Duration

	mu   sync.Mutex
	last int
	gens map[int]map[string]Node
}

// NewService returns a service over a bus, using desktop to find the focused window and driver for the actions that
// need a pointer or a keyboard. driver may be nil: those actions then fail as unsupported.
func NewService(bus Bus, desktop Desktop, driver Driver) *Service {
	return &Service{bus: bus, desktop: desktop, driver: driver, actionTimeout: defaultActionTimeout, gens: map[int]map[string]Node{}}
}

var (
	reGeneration = regexp.MustCompile(`^g[0-9]{1,9}$`)
	reElementID  = regexp.MustCompile(`^a[0-9]{1,5}$`)
)

// read walks the elements of a scope. It is the part of Tree that starts no generation.
func (s *Service) read(ctx context.Context, scope string, o Options) (Tree, string, error) {
	if utf8.RuneCountInString(o.Query) > maxQueryRunes {
		return Tree{}, "", fail(KindInvalid, "query is longer than %d characters", maxQueryRunes)
	}
	if o.MaxNodes < 0 || o.MaxDepth < 0 {
		return Tree{}, "", fail(KindInvalid, "max_nodes and max_depth cannot be negative")
	}
	var roots []Ref
	var note string
	var err error
	switch scope {
	case "", ScopeActiveWindow:
		roots, note, err = s.activeRoots(ctx)
	case ScopeDesktop:
		roots, err = s.desktopRoots(ctx)
		if err == nil && len(roots) == 0 {
			note = "no application window is registered with the accessibility service"
		}
	case ScopeBrowser:
		return Tree{}, "", fail(KindInvalid, "the page in the browser is read with the browser tool, not with this scope")
	default:
		return Tree{}, "", fail(KindInvalid, "scope %q is not active_window, desktop or browser", scope)
	}
	if err != nil {
		return Tree{}, "", err
	}

	var tree Tree
	if len(roots) > 0 {
		if tree, err = Walk(ctx, s.bus, roots, o); err != nil {
			return Tree{}, "", err
		}
	}
	if err := ctx.Err(); err != nil {
		return Tree{}, "", err
	}
	return tree, note, nil
}

// Probe says whether an element with the text in its role, name, description or value is in a scope. It starts no
// generation, so it can be asked again and again (waiting for something to appear) without making stale the ids a
// model holds from a tree it read before.
func (s *Service) Probe(ctx context.Context, scope, text string, budget time.Duration) (bool, error) {
	tree, _, err := s.read(ctx, scope, Options{Query: text, MaxNodes: 1, Budget: budget})
	return len(tree.Nodes) > 0, err
}

// Tree reads the elements of a scope and starts a new generation.
func (s *Service) Tree(ctx context.Context, scope string, o Options) (Result, error) {
	tree, note, err := s.read(ctx, scope, o)
	if err != nil {
		return Result{}, err
	}

	nodes := make(map[string]Node, len(tree.Nodes))
	res := Result{Entries: make([]Entry, 0, len(tree.Nodes)), Truncated: tree.Truncated, Note: note}
	for i, n := range tree.Nodes {
		id := "a" + strconv.Itoa(i+1)
		nodes[id] = n
		res.Entries = append(res.Entries, Entry{ID: id, Node: n})
	}
	res.Generation = s.keep(nodes)
	return res, nil
}

// keep stores a generation and returns its name, forgetting the oldest ones.
func (s *Service) keep(nodes map[string]Node) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last++
	s.gens[s.last] = nodes
	delete(s.gens, s.last-keptGenerations)
	return "g" + strconv.Itoa(s.last)
}

// Generation returns the elements of a kept generation (for the marks drawn on a screenshot).
func (s *Service) Generation(name string) (map[string]Node, bool) {
	if !reGeneration.MatchString(name) {
		return nil, false
	}
	n, err := strconv.Atoi(name[1:])
	if err != nil {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.gens[n]
	return g, ok
}

// frames lists the windows of an application that are on screen.
func (s *Service) frames(ctx context.Context, app App) ([]frame, error) {
	kids, err := s.bus.Children(ctx, app.Ref)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, nil // an application that went away while it was being asked
	}
	var out []frame
	for _, k := range kids {
		info, err := s.bus.Info(ctx, k)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		if info.State.Has(StateDefunct) || !info.State.Showing() {
			continue
		}
		out = append(out, frame{ref: k, info: info})
	}
	return out, nil
}

type frame struct {
	ref  Ref
	info Info
}

func (s *Service) apps(ctx context.Context) ([]App, error) {
	apps, err := s.bus.Apps(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("listing the applications: %w", err)
	}
	return apps, nil
}

// desktopRoots are the windows of every application.
func (s *Service) desktopRoots(ctx context.Context) ([]Ref, error) {
	apps, err := s.apps(ctx)
	if err != nil {
		return nil, err
	}
	var roots []Ref
	for _, a := range apps {
		fs, err := s.frames(ctx, a)
		if err != nil {
			return nil, err
		}
		for _, f := range fs {
			roots = append(roots, f.ref)
		}
	}
	return roots, nil
}

// activeRoots is the window that has the focus. Its application is the one whose process owns the window; of that
// application's windows, the one the application calls active, else the one with the window's title.
func (s *Service) activeRoots(ctx context.Context) ([]Ref, string, error) {
	var w *Window
	if s.desktop != nil {
		var err error
		if w, err = s.desktop.ActiveWindow(ctx); err != nil {
			if ctx.Err() != nil {
				return nil, "", ctx.Err()
			}
			return nil, "", fmt.Errorf("finding the active window: %w", err)
		}
	}
	if w == nil {
		return nil, "no window has the keyboard focus; use scope desktop to read every window", nil
	}
	apps, err := s.apps(ctx)
	if err != nil {
		return nil, "", err
	}
	var own []App
	for _, a := range apps {
		if w.PID > 0 && a.PID == w.PID {
			own = append(own, a)
		}
	}
	byPID := len(own) > 0
	if !byPID {
		own = apps
	}
	var all []frame
	for _, a := range own {
		fs, err := s.frames(ctx, a)
		if err != nil {
			return nil, "", err
		}
		all = append(all, fs...)
	}

	var active, titled []Ref
	for _, f := range all {
		if f.info.State.Has(StateActive) {
			active = append(active, f.ref)
		}
		if w.Title != "" && textclean.Line(f.info.Name, maxTitleRunes) == w.Title {
			titled = append(titled, f.ref)
		}
	}
	switch {
	case byPID && len(active) > 0: // among applications picked by process; an "active" window of another one is not the focus
		return active, "", nil
	case len(titled) > 0:
		return titled, "", nil
	case byPID && len(all) > 0:
		roots := make([]Ref, len(all))
		for i, f := range all {
			roots[i] = f.ref
		}
		return roots, "", nil
	}
	return nil, fmt.Sprintf("the application of the active window (%q) does not expose accessibility information; use the screenshot, or OCR", w.Title), nil
}

// maxTitleRunes is how much of a window title is compared (the window manager's titles are cut at this length).
const maxTitleRunes = 256

// ---- actions ------------------------------------------------------------------------------------------------------

// Outcome says how an action was done.
type Outcome struct{ Method string }

// Act does an operation on an element of a kept generation. Before anything is done the element is read again: if it
// has gone, is off screen, or is no longer what it was shown as, the action is refused as stale.
func (s *Service) Act(ctx context.Context, id, generation, op, text string) (Outcome, error) {
	if !reElementID.MatchString(id) {
		return Outcome{}, fail(KindInvalid, "id %q is not an accessibility id (a and a number, as in a12)", id)
	}
	if !reGeneration.MatchString(generation) {
		return Outcome{}, fail(KindInvalid, "generation %q is not a generation (g and a number, as in g3)", generation)
	}
	switch op {
	case OpClick, OpFocus:
		if text != "" {
			return Outcome{}, fail(KindInvalid, "%s takes no text", op)
		}
	case OpSetText:
		if err := checkText(text); err != nil {
			return Outcome{}, err
		}
	default:
		return Outcome{}, fail(KindInvalid, "op %q is not click, focus or set_text", op)
	}

	nodes, ok := s.Generation(generation)
	if !ok {
		return Outcome{}, fail(KindStale, "generation %s is stale; read the accessibility tree again", generation)
	}
	node, ok := nodes[id]
	if !ok {
		return Outcome{}, fail(KindNotFound, "generation %s has no element %s", generation, id)
	}
	info, err := s.verify(ctx, node)
	if err != nil {
		return Outcome{}, err
	}
	switch op {
	case OpClick:
		return s.click(ctx, node)
	case OpFocus:
		return s.focus(ctx, node)
	}
	return s.setText(ctx, node, info, text)
}

// checkText accepts text a person could type into a field: valid UTF-8, line breaks and tabs, no other control
// characters, a bounded length.
func checkText(text string) error {
	if !utf8.ValidString(text) {
		return fail(KindInvalid, "text is not valid UTF-8")
	}
	if utf8.RuneCountInString(text) > MaxSetTextRunes {
		return fail(KindInvalid, "text is longer than %d characters", MaxSetTextRunes)
	}
	for _, r := range text {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return fail(KindInvalid, "text contains a control character (U+%04X)", r)
		}
	}
	return nil
}

// verify reads the element again and compares it with what the model was shown.
func (s *Service) verify(ctx context.Context, n Node) (Info, error) {
	info, err := s.bus.Info(ctx, n.Ref)
	switch {
	case ctx.Err() != nil:
		return Info{}, ctx.Err()
	case err != nil:
		return Info{}, fail(KindStale, "the element %q is gone; read the accessibility tree again", n.Name)
	case info.State.Has(StateDefunct) || !info.State.Showing():
		return Info{}, fail(KindStale, "the element %q is no longer on screen; read the accessibility tree again", n.Name)
	case RoleName(info.Role) != n.Role || textclean.Line(info.Name, maxNameRunes) != n.Name:
		return Info{}, fail(KindStale, "the element %q has changed; read the accessibility tree again", n.Name)
	}
	return info, nil
}

// roles whose own action is the right way to click them. Lists, trees, tables and text are better clicked: their
// actions mean "open" or "edit", not "select".
var actionClickRoles = map[string]bool{
	"button": true, "toggle button": true, "check box": true, "check menu item": true, "radio button": true,
	"radio menu item": true, "menu item": true, "tear off menu item": true, "menu": true, "link": true, "page tab": true,
	"switch": true, "push button menu": true,
}

// pointerFirstRoles are the roles that are clicked with the pointer when they are on the screen, and by their own action only when
// they are not (or the pointer fails, which an element with no area does). A button's action is not always what a click does: on the real guest, the action of the XFCE
// panel's launchers (role "button") answered "done" and started nothing, while a click with the pointer started the program.
var pointerFirstRoles = map[string]bool{"button": true, "toggle button": true}

// the names AT-SPI actions have for "click", most direct first (GTK says click, Qt press, a few say activate).
var clickActions = []string{"click", "press", "activate", "jump", "toggle", "check", "uncheck", "push", "switch", "select", "open"}

// preferredAction picks the action that clicks: its index and name.
func preferredAction(actions []string) (int, string, bool) {
	for _, want := range clickActions {
		for i, a := range actions {
			if strings.EqualFold(strings.TrimSpace(a), want) {
				return i, want, true
			}
		}
	}
	return 0, "", false
}

func (s *Service) click(ctx context.Context, n Node) (Outcome, error) {
	if pointerFirstRoles[n.Role] {
		err := s.pointerClick(ctx, n)
		if ctx.Err() != nil {
			return Outcome{}, ctx.Err()
		}
		if err == nil {
			return Outcome{Method: "pointer"}, nil
		}
		// The pointer could not do it (the element is off the screen, say): its own action still may.
	}
	if actionClickRoles[n.Role] {
		if acts, err := s.bus.Actions(ctx, n.Ref); err == nil {
			if i, name, ok := preferredAction(acts); ok {
				done, err := s.doAction(ctx, n.Ref, i)
				if ctx.Err() != nil {
					return Outcome{}, ctx.Err()
				}
				if err == nil && done {
					return Outcome{Method: "action:" + name}, nil
				}
			}
		}
	}
	if err := s.pointerClick(ctx, n); err != nil {
		return Outcome{}, err
	}
	return Outcome{Method: "pointer"}, nil
}

// doAction asks the element to act and waits a short while for its answer. An element that is still busy with the
// action when the time is up (a button that opened a dialog and waits in it) did it: that is a success.
func (s *Service) doAction(ctx context.Context, r Ref, index int) (bool, error) {
	actx, cancel := context.WithTimeout(ctx, s.actionTimeout)
	defer cancel()
	done, err := s.bus.DoAction(actx, r, index)
	if err != nil && ctx.Err() == nil && errors.Is(actx.Err(), context.DeadlineExceeded) {
		return true, nil
	}
	return done, err
}

// pointerClick clicks the middle of the element, as it is now.
func (s *Service) pointerClick(ctx context.Context, n Node) error {
	if s.driver == nil {
		return fail(KindUnsupported, "the element %q has no action and no pointer is available", n.Name)
	}
	x, y, w, h := n.X, n.Y, n.W, n.H
	if nx, ny, nw, nh, err := s.bus.Extents(ctx, n.Ref); err == nil {
		x, y, w, h = nx, ny, nw, nh
	}
	if w <= 0 || h <= 0 {
		return fail(KindFailed, "the element %q has no area to click", n.Name)
	}
	if err := s.driver.Click(ctx, x+w/2, y+h/2); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fail(KindFailed, "clicking %q: %v", n.Name, err)
	}
	return nil
}

func (s *Service) focus(ctx context.Context, n Node) (Outcome, error) {
	took, err := s.bus.GrabFocus(ctx, n.Ref)
	if ctx.Err() != nil {
		return Outcome{}, ctx.Err()
	}
	if err == nil && took {
		return Outcome{Method: "action:focus"}, nil
	}
	// An element that takes text is focused by clicking it; clicking anything else may press it.
	if textRoles[n.Role] || n.Role == "password text" || n.Role == "combo box" {
		if err := s.pointerClick(ctx, n); err != nil {
			return Outcome{}, err
		}
		return Outcome{Method: "pointer"}, nil
	}
	return Outcome{}, fail(KindUnsupported, "the element %q does not take the keyboard focus", n.Name)
}

func (s *Service) setText(ctx context.Context, n Node, info Info, text string) (Outcome, error) {
	if !info.State.Has(StateEditable) {
		return Outcome{}, fail(KindInvalid, "the element %q (%s) is not an editable text field", n.Name, n.Role)
	}
	ok, err := s.bus.SetText(ctx, n.Ref, text)
	if ctx.Err() != nil {
		return Outcome{}, ctx.Err()
	}
	if err == nil && ok && s.holds(ctx, n, text) {
		return Outcome{Method: "editable_text"}, nil
	}
	// The field would not take the text (no EditableText, or it said no, or it kept something else): do what a person
	// does, select everything and type.
	if s.driver == nil {
		return Outcome{}, fail(KindUnsupported, "the element %q does not accept text directly and no keyboard is available", n.Name)
	}
	if _, err := s.focus(ctx, n); err != nil {
		return Outcome{}, err
	}
	if err := s.driver.Keys(ctx, "ctrl+a"); err != nil {
		return Outcome{}, s.driverFailure(ctx, n, err)
	}
	if text == "" {
		err = s.driver.Keys(ctx, "BackSpace")
	} else {
		err = s.driver.Type(ctx, text)
	}
	if err != nil {
		return Outcome{}, s.driverFailure(ctx, n, err)
	}
	return Outcome{Method: "keyboard"}, nil
}

func (s *Service) driverFailure(ctx context.Context, n Node, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fail(KindFailed, "typing into %q: %v", n.Name, err)
}

// holds reports whether the field now has exactly this text. A password field is never read, so it is trusted.
func (s *Service) holds(ctx context.Context, n Node, text string) bool {
	if n.Role == "password text" {
		return true
	}
	got, err := s.bus.Text(ctx, n.Ref, utf8.RuneCountInString(text)+1)
	return err == nil && got == text
}

// ---- marks --------------------------------------------------------------------------------------------------------

// Mark is an element to draw a numbered box around on a screenshot.
type Mark struct {
	ID         string
	X, Y, W, H int
}

// maxMarks bounds the boxes on one picture: more than this and the picture is more box than screen.
const maxMarks = 100

// Marks lists the elements of a kept generation that a person can operate, in document order, for the set-of-marks
// overlay. It reports false when the generation is not kept.
func (s *Service) Marks(generation string) ([]Mark, bool) {
	nodes, ok := s.Generation(generation)
	if !ok {
		return nil, false
	}
	type numbered struct {
		n int
		m Mark
	}
	var list []numbered
	for id, n := range nodes {
		if n.W <= 0 || n.H <= 0 || (!operable(n.Role) && len(n.Actions) == 0) {
			continue
		}
		k, err := strconv.Atoi(id[1:])
		if err != nil {
			continue
		}
		list = append(list, numbered{k, Mark{ID: id, X: n.X, Y: n.Y, W: n.W, H: n.H}})
	}
	slices.SortFunc(list, func(a, b numbered) int { return a.n - b.n })
	if len(list) > maxMarks {
		list = list[:maxMarks]
	}
	out := make([]Mark, len(list))
	for i, e := range list {
		out[i] = e.m
	}
	return out, true
}
