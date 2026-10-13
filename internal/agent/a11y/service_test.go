// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package a11y

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeDesktop struct {
	w   *Window
	err error
}

func (d *fakeDesktop) ActiveWindow(context.Context) (*Window, error) { return d.w, d.err }

type fakeDriver struct {
	mu     sync.Mutex
	clicks [][2]int
	keys   []string
	typed  []string
	err    error
}

func (d *fakeDriver) Click(_ context.Context, x, y int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.clicks = append(d.clicks, [2]int{x, y})
	return d.err
}

func (d *fakeDriver) Keys(_ context.Context, combo string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.keys = append(d.keys, combo)
	return d.err
}

func (d *fakeDriver) Type(_ context.Context, text string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.typed = append(d.typed, text)
	return d.err
}

// world is two applications on one bus:
//
//	Files (pid 100)  frame "Files" [active]: button "Open" [click], list item "report.txt", entry "Name" (editable, "old")
//	Editor (pid 200) frame "Editor": entry "Body" (editable, multi line)
type world struct {
	bus     *fakeBus
	desktop *fakeDesktop
	driver  *fakeDriver
	svc     *Service

	files, editor           Ref
	open, item, name, body  Ref
	filesFrame, editorFrame Ref
}

func newWorld() *world {
	b := newFakeBus()
	w := &world{bus: b, desktop: &fakeDesktop{w: &Window{PID: 100, Title: "Files"}}, driver: &fakeDriver{}}
	w.files = b.add("files-app", fakeNode{role: roleApp, name: "Files"})
	w.editor = b.add("editor-app", fakeNode{role: roleApp, name: "Editor"})
	b.apps = []App{{Ref: w.files, Name: "Files", PID: 100}, {Ref: w.editor, Name: "Editor", PID: 200}}

	w.filesFrame = b.add("files-frame", fakeNode{role: roleFrame, name: "Files", state: shown | 1<<StateActive, x: 0, y: 0, w: 800, h: 600})
	b.child(w.files, w.filesFrame)
	w.open = b.add("open", fakeNode{role: rolePushButton, name: "Open", x: 100, y: 50, w: 60, h: 20, actions: []string{"click"}})
	w.item = b.add("item", fakeNode{role: roleListItem, name: "report.txt", x: 10, y: 100, w: 200, h: 20, actions: []string{"activate"}})
	w.name = b.add("name", fakeNode{role: roleEntry, name: "Name", text: "old", x: 300, y: 40, w: 100, h: 24, state: shown | 1<<StateEditable})
	b.child(w.filesFrame, w.open)
	b.child(w.filesFrame, w.item)
	b.child(w.filesFrame, w.name)

	w.editorFrame = b.add("editor-frame", fakeNode{role: roleFrame, name: "Editor", x: 50, y: 50, w: 700, h: 500})
	b.child(w.editor, w.editorFrame)
	w.body = b.add("body", fakeNode{role: roleEntry, name: "Body", text: "hello", x: 60, y: 80, w: 680, h: 400, state: shown | 1<<StateEditable | 1<<StateMultiLine})
	b.child(w.editorFrame, w.body)

	w.svc = NewService(b, w.desktop, w.driver)
	w.svc.actionTimeout = 80 * time.Millisecond
	return w
}

func ids(r Result) []string {
	out := make([]string, len(r.Entries))
	for i, e := range r.Entries {
		out[i] = e.ID + "=" + e.Role + ":" + e.Name
	}
	return out
}

func kindOf(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return -1
}

func (w *world) tree(t *testing.T, scope string) Result {
	t.Helper()
	r, err := w.svc.Tree(context.Background(), scope, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestTreeOfTheActiveWindow(t *testing.T) {
	w := newWorld()
	r := w.tree(t, "")
	want := []string{"a1=frame:Files", "a2=button:Open", "a3=list item:report.txt", "a4=entry:Name"}
	if got := ids(r); !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if r.Generation != "g1" || r.Truncated || r.Note != "" {
		t.Errorf("%+v", r)
	}
	if r2 := w.tree(t, ScopeActiveWindow); r2.Generation != "g2" || len(r2.Entries) != 4 {
		t.Errorf("a new generation each time: %+v", r2)
	}
	if got := r.Entries[3].Value; got != "old" {
		t.Errorf("the field's text is part of the tree: %q", got)
	}
	// The other window is not part of it.
	for _, e := range r.Entries {
		if strings.Contains(e.Name, "Body") || e.Name == "Editor" {
			t.Errorf("%s from another application", e.Name)
		}
	}
}

func TestTreeFollowsTheFocus(t *testing.T) {
	w := newWorld()
	w.desktop.w = &Window{PID: 200, Title: "Editor"}
	got := ids(w.tree(t, ""))
	if want := []string{"a1=frame:Editor", "a2=entry:Body"}; !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestActiveWindowIsPickedByStateThenByTitle(t *testing.T) {
	w := newWorld()
	// A second window of the same application.
	second := w.bus.add("files-frame-2", fakeNode{role: roleFrame, name: "Downloads"})
	w.bus.child(w.files, second)
	w.bus.child(second, w.bus.add("dl-button", fakeNode{role: rolePushButton, name: "Clear"}))

	if got := ids(w.tree(t, "")); got[0] != "a1=frame:Files" || len(got) != 4 {
		t.Errorf("the window the application calls active: %v", got)
	}

	w.bus.nodes[w.filesFrame].state = shown // nobody is active any more
	w.desktop.w = &Window{PID: 100, Title: "Downloads"}
	if got := ids(w.tree(t, "")); !slices.Equal(got, []string{"a1=frame:Downloads", "a2=button:Clear"}) {
		t.Errorf("by title: %v", got)
	}

	w.desktop.w = &Window{PID: 100, Title: "Something else"}
	if r := w.tree(t, ""); len(r.Entries) == 0 || r.Note != "" {
		t.Errorf("an application whose window is known by its process is read whole: %+v", r)
	}

	// The process is not known, but the title is.
	w.desktop.w = &Window{PID: 4242, Title: "Editor"}
	if got := ids(w.tree(t, "")); len(got) != 2 || got[0] != "a1=frame:Editor" {
		t.Errorf("by title across applications: %v", got)
	}
}

func TestTreeSaysWhyItIsEmpty(t *testing.T) {
	w := newWorld()
	w.desktop.w = nil
	r := w.tree(t, "")
	if len(r.Entries) != 0 || !strings.Contains(r.Note, "no window has the keyboard focus") || r.Generation == "" {
		t.Errorf("no focused window: %+v", r)
	}

	w.desktop.w = &Window{PID: 999, Title: "Chromium"}
	r = w.tree(t, "")
	if len(r.Entries) != 0 || !strings.Contains(r.Note, "does not expose accessibility information") || !strings.Contains(r.Note, "Chromium") {
		t.Errorf("an application that is not on the bus: %+v", r)
	}

	w.bus.apps = nil
	w.desktop.w = &Window{PID: 100, Title: "Files"}
	if r = w.tree(t, ScopeDesktop); len(r.Entries) != 0 || r.Note == "" {
		t.Errorf("no applications: %+v", r)
	}
}

func TestTreeOfTheDesktop(t *testing.T) {
	w := newWorld()
	hidden := w.bus.add("hidden-frame", fakeNode{role: roleFrame, name: "Hidden", state: 1 << StateEnabled})
	w.bus.child(w.files, hidden)
	got := ids(w.tree(t, ScopeDesktop))
	want := []string{"a1=frame:Files", "a2=button:Open", "a3=list item:report.txt", "a4=entry:Name", "a5=frame:Editor", "a6=entry:Body"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestTreeRefusesWhatItCannotServe(t *testing.T) {
	w := newWorld()
	for name, call := range map[string]func() error{
		"browser": func() error { _, err := w.svc.Tree(context.Background(), ScopeBrowser, Options{}); return err },
		"unknown": func() error { _, err := w.svc.Tree(context.Background(), "everything", Options{}); return err },
		"query": func() error {
			_, err := w.svc.Tree(context.Background(), "", Options{Query: strings.Repeat("q", 101)})
			return err
		},
		"nodes": func() error { _, err := w.svc.Tree(context.Background(), "", Options{MaxNodes: -1}); return err },
		"depth": func() error { _, err := w.svc.Tree(context.Background(), "", Options{MaxDepth: -1}); return err },
	} {
		if err := call(); kindOf(err) != KindInvalid {
			t.Errorf("%s: %v", name, err)
		}
	}
	if w.svc.last != 0 {
		t.Errorf("a refused request must not use up a generation (%d)", w.svc.last)
	}
}

func TestTreeQueryAndLimits(t *testing.T) {
	w := newWorld()
	r, err := w.svc.Tree(context.Background(), "", Options{Query: "open"})
	if err != nil || !slices.Equal(ids(r), []string{"a1=button:Open"}) {
		t.Errorf("%v %v", ids(r), err)
	}
	r, err = w.svc.Tree(context.Background(), "", Options{MaxNodes: 2})
	if err != nil || len(r.Entries) != 2 || !r.Truncated {
		t.Errorf("%v %v %v", ids(r), r.Truncated, err)
	}
}

func TestTreeFailsOnBusProblems(t *testing.T) {
	w := newWorld()
	w.desktop.err = errBoom
	if _, err := w.svc.Tree(context.Background(), "", Options{}); !errors.Is(err, errBoom) {
		t.Errorf("a failure to find the active window is reported: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w.desktop.err = nil
	if _, err := w.svc.Tree(ctx, "", Options{}); !errors.Is(err, context.Canceled) {
		t.Errorf("a canceled request is a cancellation, not a tree: %v", err)
	}
}

func TestGenerationsAreKeptAFewAtATime(t *testing.T) {
	w := newWorld()
	for range 6 {
		w.tree(t, "")
	}
	for g, want := range map[string]bool{"g1": false, "g2": false, "g3": true, "g4": true, "g5": true, "g6": true, "g7": false, "bad": false, "g": false, "g99999999999": false} {
		if _, ok := w.svc.Generation(g); ok != want {
			t.Errorf("generation %s kept=%v, want %v", g, ok, want)
		}
	}
	if _, err := w.svc.Act(context.Background(), "a2", "g1", OpClick, ""); kindOf(err) != KindStale {
		t.Errorf("an old generation is stale: %v", err)
	}
	if _, err := w.svc.Act(context.Background(), "a2", "g6", OpClick, ""); err != nil {
		t.Errorf("the latest works: %v", err)
	}
	if _, err := w.svc.Act(context.Background(), "a2", "g3", OpClick, ""); err != nil {
		t.Errorf("a kept earlier one works while the element is unchanged: %v", err)
	}
}

func TestActRefusesBadRequests(t *testing.T) {
	w := newWorld()
	g := w.tree(t, "").Generation
	long := strings.Repeat("x", MaxSetTextRunes+1)
	tests := []struct {
		name              string
		id, gen, op, text string
		kind              Kind
	}{
		{"id", "x1", g, OpClick, "", KindInvalid},
		{"id with a path", "a1/../a2", g, OpClick, "", KindInvalid},
		{"id empty", "", g, OpClick, "", KindInvalid},
		{"id too long", "a123456", g, OpClick, "", KindInvalid},
		{"generation", "a1", "3", OpClick, "", KindInvalid},
		{"generation empty", "a1", "", OpClick, "", KindInvalid},
		{"op", "a1", g, "hover", "", KindInvalid},
		{"click with text", "a2", g, OpClick, "x", KindInvalid},
		{"focus with text", "a4", g, OpFocus, "x", KindInvalid},
		{"text with a control character", "a4", g, OpSetText, "a\x07b", KindInvalid},
		{"text with an escape", "a4", g, OpSetText, "\x1b[2J", KindInvalid},
		{"text not UTF-8", "a4", g, OpSetText, "bad \xff", KindInvalid},
		{"text too long", "a4", g, OpSetText, long, KindInvalid},
		{"unknown generation", "a2", "g77", OpClick, "", KindStale},
		{"no such element", "a99", g, OpClick, "", KindNotFound},
	}
	for _, tt := range tests {
		_, err := w.svc.Act(context.Background(), tt.id, tt.gen, tt.op, tt.text)
		if kindOf(err) != tt.kind {
			t.Errorf("%s: %v (kind %d, want %d)", tt.name, err, kindOf(err), tt.kind)
		}
	}
	if len(w.bus.done)+len(w.driver.clicks)+len(w.driver.typed) != 0 || len(w.bus.setTexts) != 0 {
		t.Error("a refused request did something")
	}
	if _, err := w.svc.Act(context.Background(), "a4", g, OpSetText, "line one\n\tline two"); err != nil {
		t.Errorf("line breaks and tabs are fine: %v", err)
	}
}

func TestClickUsesTheElementsOwnAction(t *testing.T) {
	w := newWorld()
	g := w.tree(t, "").Generation
	out, err := w.svc.Act(context.Background(), "a2", g, OpClick, "")
	if err != nil || out.Method != "action:click" {
		t.Fatalf("%+v %v", out, err)
	}
	if len(w.bus.done) != 1 || !strings.HasSuffix(w.bus.done[0], "open#0:click") || len(w.driver.clicks) != 0 {
		t.Errorf("actions %v, clicks %v", w.bus.done, w.driver.clicks)
	}
}

func TestClickPicksTheRightActionByName(t *testing.T) {
	for _, tt := range []struct {
		actions []string
		index   int
		name    string
		ok      bool
	}{
		{[]string{"click"}, 0, "click", true},
		{[]string{"Press"}, 0, "press", true},
		{[]string{"menu", "Activate"}, 1, "activate", true},
		{[]string{"edit", "click", "press"}, 1, "click", true},
		{[]string{"jump"}, 0, "jump", true},
		{[]string{" toggle "}, 0, "toggle", true},
		{[]string{"expand or contract"}, 0, "", false},
		{nil, 0, "", false},
	} {
		i, name, ok := preferredAction(tt.actions)
		if ok != tt.ok || (ok && (i != tt.index || name != tt.name)) {
			t.Errorf("%v: %d %q %v", tt.actions, i, name, ok)
		}
	}
}

func TestClickOnAListItemClicksTheCentre(t *testing.T) {
	w := newWorld()
	g := w.tree(t, "").Generation
	// The list scrolled since the tree was read: the click goes where the item is now.
	w.bus.nodes[w.item].y = 300
	out, err := w.svc.Act(context.Background(), "a3", g, OpClick, "")
	if err != nil || out.Method != "pointer" {
		t.Fatalf("%+v %v", out, err)
	}
	if want := [][2]int{{10 + 100, 300 + 10}}; !slices.Equal(w.driver.clicks, want) {
		t.Errorf("clicked at %v, want %v: selecting is not what 'activate' does", w.driver.clicks, want)
	}
	if len(w.bus.done) != 0 {
		t.Errorf("the item's 'activate' must not be used for a click: %v", w.bus.done)
	}
}

func TestClickFallsBackToThePointerWhenTheActionFails(t *testing.T) {
	w := newWorld()
	g := w.tree(t, "").Generation
	w.bus.actOK = false
	out, err := w.svc.Act(context.Background(), "a2", g, OpClick, "")
	if err != nil || out.Method != "pointer" || !slices.Equal(w.driver.clicks, [][2]int{{130, 60}}) {
		t.Errorf("%+v %v %v", out, err, w.driver.clicks)
	}
}

func TestClickDoesNotWaitForAButtonThatOpenedADialog(t *testing.T) {
	w := newWorld()
	g := w.tree(t, "").Generation
	w.bus.blockAction = true
	start := time.Now()
	out, err := w.svc.Act(context.Background(), "a2", g, OpClick, "")
	if err != nil || out.Method != "action:click" {
		t.Fatalf("%+v %v", out, err)
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("waited %v", took)
	}
	if len(w.driver.clicks) != 0 {
		t.Errorf("an action that was dispatched must not be followed by a second click: %v", w.driver.clicks)
	}
}

func TestClickEndsWhenTheRequestIsCanceled(t *testing.T) {
	w := newWorld()
	g := w.tree(t, "").Generation
	w.bus.blockAction = true
	w.svc.actionTimeout = time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	if _, err := w.svc.Act(ctx, "a2", g, OpClick, ""); !errors.Is(err, context.Canceled) {
		t.Errorf("%v", err)
	}
	if len(w.driver.clicks) != 0 {
		t.Error("a canceled request must not fall back to clicking")
	}
}

func TestClickWithoutAnAreaOrAPointer(t *testing.T) {
	w := newWorld()
	g := w.tree(t, "").Generation
	w.bus.nodes[w.item].w, w.bus.nodes[w.item].h = 0, 0
	if _, err := w.svc.Act(context.Background(), "a3", g, OpClick, ""); kindOf(err) != KindFailed {
		t.Errorf("an element with no area cannot be clicked: %v", err)
	}
	w.bus.nodes[w.item].w, w.bus.nodes[w.item].h = 200, 20
	svc := NewService(w.bus, w.desktop, nil)
	g = func() string { r, _ := svc.Tree(context.Background(), "", Options{}); return r.Generation }()
	if _, err := svc.Act(context.Background(), "a3", g, OpClick, ""); kindOf(err) != KindUnsupported {
		t.Errorf("no pointer: %v", err)
	}
	w.driver.err = errBoom
	g = w.tree(t, "").Generation
	if _, err := w.svc.Act(context.Background(), "a3", g, OpClick, ""); kindOf(err) != KindFailed || !strings.Contains(err.Error(), "boom") {
		t.Errorf("a pointer that fails: %v", err)
	}
}

func TestActionsRefuseAnElementThatIsNotWhatWasShown(t *testing.T) {
	cases := map[string]func(w *world){
		"gone":         func(w *world) { delete(w.bus.nodes, w.open) },
		"defunct":      func(w *world) { w.bus.nodes[w.open].state |= 1 << StateDefunct },
		"off screen":   func(w *world) { w.bus.nodes[w.open].state = 1 << StateEnabled },
		"renamed":      func(w *world) { w.bus.nodes[w.open].name = "Delete everything" },
		"another role": func(w *world) { w.bus.nodes[w.open].role = roleCheckBox },
		"info fails":   func(w *world) { w.bus.nodes[w.open].infoErr = errBoom },
	}
	for name, change := range cases {
		w := newWorld()
		g := w.tree(t, "").Generation
		change(w)
		for _, op := range []string{OpClick, OpFocus} {
			if _, err := w.svc.Act(context.Background(), "a2", g, op, ""); kindOf(err) != KindStale {
				t.Errorf("%s/%s: %v", name, op, err)
			}
		}
		if len(w.bus.done)+len(w.bus.focused)+len(w.driver.clicks) != 0 {
			t.Errorf("%s: a stale element was acted on", name)
		}
	}
}

func TestFocus(t *testing.T) {
	w := newWorld()
	g := w.tree(t, "").Generation
	out, err := w.svc.Act(context.Background(), "a4", g, OpFocus, "")
	if err != nil || out.Method != "action:focus" || len(w.bus.focused) != 1 {
		t.Errorf("%+v %v %v", out, err, w.bus.focused)
	}

	w.bus.actOK = false
	out, err = w.svc.Act(context.Background(), "a4", g, OpFocus, "")
	if err != nil || out.Method != "pointer" || !slices.Equal(w.driver.clicks, [][2]int{{350, 52}}) {
		t.Errorf("a field that refuses the focus is clicked: %+v %v %v", out, err, w.driver.clicks)
	}
	w.driver.clicks = nil
	if _, err = w.svc.Act(context.Background(), "a2", g, OpFocus, ""); kindOf(err) != KindUnsupported || len(w.driver.clicks) != 0 {
		t.Errorf("a button that refuses the focus is not clicked (that would press it): %v %v", err, w.driver.clicks)
	}
}

func TestSetTextDirectly(t *testing.T) {
	w := newWorld()
	g := w.tree(t, "").Generation
	out, err := w.svc.Act(context.Background(), "a4", g, OpSetText, "new name")
	if err != nil || out.Method != "editable_text" {
		t.Fatalf("%+v %v", out, err)
	}
	if got := w.bus.setTexts[w.name]; got != "new name" || len(w.driver.keys)+len(w.driver.typed) != 0 {
		t.Errorf("set %q, keys %v, typed %v", got, w.driver.keys, w.driver.typed)
	}
	if out, err = w.svc.Act(context.Background(), "a4", g, OpSetText, ""); err != nil || out.Method != "editable_text" || w.bus.nodes[w.name].text != "" {
		t.Errorf("an empty text clears the field: %+v %v %q", out, err, w.bus.nodes[w.name].text)
	}
}

func TestSetTextTypesWhenTheFieldDoesNotKeepIt(t *testing.T) {
	w := newWorld()
	g := w.tree(t, "").Generation
	w.bus.ignoreSet = true
	out, err := w.svc.Act(context.Background(), "a4", g, OpSetText, "new name")
	if err != nil || out.Method != "keyboard" {
		t.Fatalf("%+v %v", out, err)
	}
	if !slices.Equal(w.driver.keys, []string{"ctrl+a"}) || !slices.Equal(w.driver.typed, []string{"new name"}) || len(w.bus.focused) != 1 {
		t.Errorf("focus %v, keys %v, typed %v", w.bus.focused, w.driver.keys, w.driver.typed)
	}

	w.driver.keys, w.driver.typed = nil, nil
	if out, err = w.svc.Act(context.Background(), "a4", g, OpSetText, ""); err != nil || out.Method != "keyboard" {
		t.Fatalf("%+v %v", out, err)
	}
	if !slices.Equal(w.driver.keys, []string{"ctrl+a", "BackSpace"}) || len(w.driver.typed) != 0 {
		t.Errorf("clearing by keyboard: keys %v, typed %v", w.driver.keys, w.driver.typed)
	}
}

func TestSetTextTypesWhenThereIsNoEditableTextInterface(t *testing.T) {
	w := newWorld()
	g := w.tree(t, "").Generation
	w.bus.nodes[w.name].noText = true
	out, err := w.svc.Act(context.Background(), "a4", g, OpSetText, "x")
	if err != nil || out.Method != "keyboard" {
		t.Errorf("%+v %v", out, err)
	}
	svc := NewService(w.bus, w.desktop, nil)
	r, _ := svc.Tree(context.Background(), "", Options{})
	if _, err = svc.Act(context.Background(), "a4", r.Generation, OpSetText, "x"); kindOf(err) != KindUnsupported {
		t.Errorf("neither interface nor keyboard: %v", err)
	}
}

func TestSetTextRefusesAFieldThatIsNotEditable(t *testing.T) {
	w := newWorld()
	g := w.tree(t, "").Generation
	if _, err := w.svc.Act(context.Background(), "a3", g, OpSetText, "x"); kindOf(err) != KindInvalid {
		t.Errorf("a list item: %v", err)
	}
	w.bus.nodes[w.name].state = shown // read only now
	if _, err := w.svc.Act(context.Background(), "a4", g, OpSetText, "x"); kindOf(err) != KindInvalid {
		t.Errorf("a field that is no longer editable: %v", err)
	}
	if len(w.bus.setTexts)+len(w.driver.typed) != 0 {
		t.Error("typed into a field that is not editable")
	}
}

func TestSetTextDoesNotReadBackAPassword(t *testing.T) {
	w := newWorld()
	pw := w.bus.add("pw", fakeNode{role: rolePassword, name: "Password", text: "", state: shown | 1<<StateEditable, x: 1, y: 1, w: 50, h: 20})
	w.bus.child(w.filesFrame, pw)
	g := w.tree(t, "").Generation
	var id string
	for k, n := range w.svc.gens[1] {
		if n.Ref == pw {
			id = k
		}
	}
	if id == "" {
		t.Fatal("the password field is in the tree")
	}
	before := w.bus.texts.Load()
	out, err := w.svc.Act(context.Background(), id, g, OpSetText, "s3cret")
	if err != nil || out.Method != "editable_text" {
		t.Fatalf("%+v %v", out, err)
	}
	if got := w.bus.texts.Load(); got != before {
		t.Errorf("the password field was read back (%d reads)", got-before)
	}
}

func TestConcurrentRequestsGetDistinctGenerations(t *testing.T) {
	w := newWorld()
	var wg sync.WaitGroup
	gens := make(chan string, 40)
	for range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := w.svc.Tree(context.Background(), "", Options{})
			if err != nil {
				t.Error(err)
				return
			}
			gens <- r.Generation
		}()
	}
	wg.Wait()
	close(gens)
	seen := map[string]bool{}
	for g := range gens {
		if seen[g] {
			t.Errorf("generation %s was handed out twice", g)
		}
		seen[g] = true
	}
	if len(seen) != 40 {
		t.Errorf("%d generations", len(seen))
	}
	if len(w.svc.gens) != keptGenerations {
		t.Errorf("%d kept, want %d", len(w.svc.gens), keptGenerations)
	}
}

func TestMarksAreTheOperableElementsInDocumentOrder(t *testing.T) {
	w := newWorld()
	// The window, a button, a list item, a field: the frame is not something to operate.
	g := w.tree(t, "").Generation
	marks, ok := w.svc.Marks(g)
	if !ok {
		t.Fatal("the generation is kept")
	}
	var got []string
	for _, m := range marks {
		got = append(got, m.ID)
	}
	if want := []string{"a2", "a3", "a4"}; !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if marks[0].X != 100 || marks[0].Y != 50 || marks[0].W != 60 || marks[0].H != 20 {
		t.Errorf("%+v", marks[0])
	}
	if _, ok := w.svc.Marks("g99"); ok {
		t.Error("an unknown generation has no marks")
	}
	if _, ok := w.svc.Marks("nonsense"); ok {
		t.Error("a malformed generation has no marks")
	}
}

func TestMarksAreCapped(t *testing.T) {
	b := newFakeBus()
	frame := b.add("frame", fakeNode{role: roleFrame, name: "Many"})
	app := b.add("app", fakeNode{role: roleApp, name: "Many"})
	b.child(app, frame)
	b.apps = []App{{Ref: app, Name: "Many", PID: 1}}
	for i := range 130 {
		b.child(frame, b.add(fmt.Sprintf("b%d", i), fakeNode{role: rolePushButton, name: fmt.Sprintf("B%d", i)}))
	}
	svc := NewService(b, &fakeDesktop{w: &Window{Title: "Many"}}, nil)
	res, err := svc.Tree(context.Background(), ScopeDesktop, Options{MaxNodes: 200})
	if err != nil || len(res.Entries) < 131 {
		t.Fatalf("%d entries, %v", len(res.Entries), err)
	}
	marks, _ := svc.Marks(res.Generation)
	if len(marks) != maxMarks || marks[0].ID != "a2" || marks[maxMarks-1].ID != fmt.Sprintf("a%d", maxMarks+1) {
		t.Errorf("%d marks, first %s, last %s", len(marks), marks[0].ID, marks[len(marks)-1].ID)
	}
}

func TestProbeFindsAnElementWithoutTakingAGeneration(t *testing.T) {
	w := newWorld()
	g := w.tree(t, "").Generation
	before := w.svc.last
	for _, tt := range []struct {
		scope, text string
		want        bool
	}{
		{"", "open", true},
		{"", "REPORT.TXT", true},
		{"", "button", true},
		{"", "nothing like this", false},
		{ScopeDesktop, "body", true}, // the other window's field
		{"", "body", false},          // not in the active window
		{ScopeActiveWindow, "name", true},
	} {
		got, err := w.svc.Probe(context.Background(), tt.scope, tt.text, 0)
		if err != nil || got != tt.want {
			t.Errorf("%q in %q: %v %v, want %v", tt.text, tt.scope, got, err, tt.want)
		}
	}
	if w.svc.last != before {
		t.Errorf("a probe starts no generation (%d -> %d)", before, w.svc.last)
	}
	if _, err := w.svc.Act(context.Background(), "a2", g, OpClick, ""); err != nil {
		t.Errorf("the ids of the tree read before the probes still work: %v", err)
	}
	if _, err := w.svc.Probe(context.Background(), ScopeBrowser, "x", 0); kindOf(err) != KindInvalid {
		t.Errorf("%v", err)
	}
	if _, err := w.svc.Probe(context.Background(), "", strings.Repeat("q", 101), 0); kindOf(err) != KindInvalid {
		t.Errorf("%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := w.svc.Probe(ctx, "", "open", 0); !errors.Is(err, context.Canceled) {
		t.Errorf("%v", err)
	}
}
