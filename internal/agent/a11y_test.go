// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/PacifAIst/controlsnack-local/internal/agent/a11y"
	"github.com/PacifAIst/controlsnack-local/internal/agent/x11"
	"github.com/PacifAIst/controlsnack-local/internal/protocol"
)

// Role numbers of AT-SPI used here.
const (
	atFrame, atListItem, atPushButton, atApp, atEntry, atMenuItem = 23, 32, 43, 75, 79, 35
)

const atShown = a11y.StateSet(1<<a11y.StateEnabled | 1<<a11y.StateSensitive | 1<<a11y.StateShowing | 1<<a11y.StateVisible)

type atNode struct {
	role     uint32
	state    a11y.StateSet
	name     string
	children []a11y.Ref
	x, y     int
	w, h     int
	actions  []string
	text     string
}

// atBus is a small accessibility tree in memory: one application with a window, a button, a list item and a field.
type atBus struct {
	nodes    map[a11y.Ref]*atNode
	apps     []a11y.App
	appsErr  error
	setTexts []string
}

func ref(p string) a11y.Ref { return a11y.Ref{Bus: ":1.5", Path: "/" + p} }

func newATBus() *atBus {
	b := &atBus{nodes: map[a11y.Ref]*atNode{}}
	root, frame, button, item, field := ref("root"), ref("frame"), ref("button"), ref("item"), ref("field")
	b.nodes[root] = &atNode{role: atApp, state: atShown, name: "Thunar", children: []a11y.Ref{frame}}
	b.nodes[frame] = &atNode{role: atFrame, state: atShown | 1<<a11y.StateActive, name: "doc - Thunar", children: []a11y.Ref{button, item, field}, w: 650, h: 514, x: 635, y: 296}
	b.nodes[button] = &atNode{role: atPushButton, state: atShown, name: "Open", w: 60, h: 20, x: 700, y: 300, actions: []string{"click"}}
	b.nodes[item] = &atNode{role: atListItem, state: atShown, name: "report.txt", w: 200, h: 20, x: 640, y: 400}
	b.nodes[field] = &atNode{role: atEntry, state: atShown | 1<<a11y.StateEditable, name: "Name", w: 100, h: 24, x: 800, y: 300, text: "old"}
	b.apps = []a11y.App{{Ref: root, Name: "Thunar", PID: 1212}}
	return b
}

func (b *atBus) n(r a11y.Ref) (*atNode, error) {
	if n, ok := b.nodes[r]; ok {
		return n, nil
	}
	return nil, a11y.ErrGone
}

func (b *atBus) Apps(context.Context) ([]a11y.App, error) { return b.apps, b.appsErr }
func (b *atBus) Info(_ context.Context, r a11y.Ref) (a11y.Info, error) {
	n, err := b.n(r)
	if err != nil {
		return a11y.Info{}, err
	}
	return a11y.Info{Role: n.role, State: n.state, Name: n.name, ChildCount: len(n.children)}, nil
}
func (b *atBus) Children(_ context.Context, r a11y.Ref) ([]a11y.Ref, error) {
	n, err := b.n(r)
	if err != nil {
		return nil, err
	}
	return n.children, nil
}
func (b *atBus) Extents(_ context.Context, r a11y.Ref) (int, int, int, int, error) {
	n, err := b.n(r)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	return n.x, n.y, n.w, n.h, nil
}
func (b *atBus) Actions(_ context.Context, r a11y.Ref) ([]string, error) {
	n, err := b.n(r)
	if err != nil {
		return nil, err
	}
	return n.actions, nil
}
func (b *atBus) Text(_ context.Context, r a11y.Ref, max int) (string, error) {
	n, err := b.n(r)
	if err != nil {
		return "", err
	}
	if n.role != atEntry {
		return "", a11y.ErrUnsupported
	}
	return n.text, nil
}
func (b *atBus) Number(context.Context, a11y.Ref) (float64, error) { return 0, a11y.ErrUnsupported }
func (b *atBus) DoAction(context.Context, a11y.Ref, int) (bool, error) {
	return true, nil
}
func (b *atBus) GrabFocus(context.Context, a11y.Ref) (bool, error) { return true, nil }
func (b *atBus) SetText(_ context.Context, r a11y.Ref, text string) (bool, error) {
	n, err := b.n(r)
	if err != nil {
		return false, err
	}
	n.text = text
	b.setTexts = append(b.setTexts, text)
	return true, nil
}
func (b *atBus) Close() error { return nil }

// a11yRig is the handlers over the tree, with the input rig as the pointer and keyboard.
type a11yRig struct {
	h   A11y
	bus *atBus
	in  *inputRig
	d   *fakeDesktop
}

func newA11yRig() *a11yRig {
	r := &a11yRig{bus: newATBus(), in: newInputRig(), d: twoWindows()}
	r.d.active = &x11.WindowInfo{ID: 1, Title: "doc - Thunar", PID: 1212, Active: true}
	r.h = NewA11y(r.bus, r.d, r.in.in)
	return r
}

func (r *a11yRig) tree(t *testing.T, req protocol.A11yTreeReq) protocol.A11yTreeRes {
	t.Helper()
	res, _, err := r.h.Tree(context.Background(), frame(t, protocol.TypeA11yTree, req))
	if err != nil {
		t.Fatal(err)
	}
	return res.(protocol.A11yTreeRes)
}

func (r *a11yRig) act(t *testing.T, req protocol.A11yActionReq) (protocol.A11yActionRes, error) {
	res, _, err := r.h.Action(context.Background(), frame(t, protocol.TypeA11yAction, req))
	if err != nil {
		return protocol.A11yActionRes{}, err
	}
	return res.(protocol.A11yActionRes), nil
}

func TestA11yTree(t *testing.T) {
	r := newA11yRig()
	res := r.tree(t, protocol.A11yTreeReq{})
	if res.Generation != "g1" || res.Truncated || res.Note != "" || len(res.Nodes) != 4 {
		t.Fatalf("%+v", res)
	}
	want := []protocol.A11yNode{
		{ID: "a1", Role: "frame", Name: "doc - Thunar", States: []string{"active"}, Bounds: protocol.Rect{635, 296, 650, 514}},
		{ID: "a2", Role: "button", Name: "Open", Bounds: protocol.Rect{700, 300, 60, 20}, Actions: []string{"click"}},
		{ID: "a3", Role: "list item", Name: "report.txt", Bounds: protocol.Rect{640, 400, 200, 20}},
		{ID: "a4", Role: "entry", Name: "Name", Value: "old", States: []string{"editable"}, Bounds: protocol.Rect{800, 300, 100, 24}},
	}
	for i, w := range want {
		g := res.Nodes[i]
		if g.ID != w.ID || g.Role != w.Role || g.Name != w.Name || g.Value != w.Value || g.Bounds != w.Bounds ||
			!slices.Equal(g.States, w.States) || !slices.Equal(g.Actions, w.Actions) {
			t.Errorf("node %d: %+v, want %+v", i, g, w)
		}
	}
	if q := r.tree(t, protocol.A11yTreeReq{Query: "open", MaxNodes: 5, MaxDepth: 3}); len(q.Nodes) != 1 || q.Nodes[0].Name != "Open" || q.Generation != "g2" {
		t.Errorf("%+v", q)
	}
	// And it is JSON the host can read back with its own types.
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var back protocol.A11yTreeRes
	if err := json.Unmarshal(b, &back); err != nil || len(back.Nodes) != 4 {
		t.Errorf("%v %+v", err, back)
	}
}

func TestA11yTreeSaysWhyItIsEmpty(t *testing.T) {
	r := newA11yRig()
	r.d.active = nil
	if res := r.tree(t, protocol.A11yTreeReq{}); len(res.Nodes) != 0 || res.Note == "" || res.Generation == "" {
		t.Errorf("%+v", res)
	}
}

func TestA11yErrorsCarryTheProtocolCodes(t *testing.T) {
	r := newA11yRig()
	g := r.tree(t, protocol.A11yTreeReq{}).Generation
	tests := []struct {
		name string
		call func() error
		code string
	}{
		{"bad scope", func() error {
			_, _, err := r.h.Tree(context.Background(), frame(t, protocol.TypeA11yTree, protocol.A11yTreeReq{Scope: "everywhere"}))
			return err
		}, protocol.CodeInvalidArgs},
		{"browser scope", func() error {
			_, _, err := r.h.Tree(context.Background(), frame(t, protocol.TypeA11yTree, protocol.A11yTreeReq{Scope: "browser"}))
			return err
		}, protocol.CodeInvalidArgs},
		{"bad body", func() error {
			_, _, err := r.h.Tree(context.Background(), frame(t, protocol.TypeA11yTree, map[string]any{"max_nodes": "many"}))
			return err
		}, protocol.CodeInvalidArgs},
		{"unknown field", func() error {
			_, _, err := r.h.Action(context.Background(), frame(t, protocol.TypeA11yAction, map[string]any{"id": "a1", "color": "red"}))
			return err
		}, protocol.CodeInvalidArgs},
		{"stale generation", func() error {
			_, err := r.act(t, protocol.A11yActionReq{ID: "a2", Generation: "g99", Op: "click"})
			return err
		}, protocol.CodeStaleElement},
		{"unknown element", func() error {
			_, err := r.act(t, protocol.A11yActionReq{ID: "a77", Generation: g, Op: "click"})
			return err
		}, protocol.CodeNotFound},
		{"bad op", func() error {
			_, err := r.act(t, protocol.A11yActionReq{ID: "a2", Generation: g, Op: "hover"})
			return err
		}, protocol.CodeInvalidArgs},
	}
	for _, tt := range tests {
		if got := code(tt.call()); got != tt.code {
			t.Errorf("%s: code %q, want %q", tt.name, got, tt.code)
		}
	}

	r.bus.appsErr = a11y.ErrNoBus
	if _, _, err := r.h.Tree(context.Background(), frame(t, protocol.TypeA11yTree, protocol.A11yTreeReq{})); code(err) != protocol.CodeUnsupported {
		t.Errorf("no bus: %v", err)
	}
	r.bus.appsErr = errors.New("connection reset")
	if _, _, err := r.h.Tree(context.Background(), frame(t, protocol.TypeA11yTree, protocol.A11yTreeReq{})); code(err) != protocol.CodeInternal || !strings.Contains(err.Error(), "connection reset") {
		t.Errorf("a bus failure: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.bus.appsErr = nil
	if _, _, err := r.h.Tree(ctx, frame(t, protocol.TypeA11yTree, protocol.A11yTreeReq{})); !errors.Is(err, context.Canceled) {
		t.Errorf("a canceled request stays a cancellation: %v", err)
	}
}

func TestA11yClickByActionAndByPointer(t *testing.T) {
	r := newA11yRig()
	g := r.tree(t, protocol.A11yTreeReq{}).Generation
	// A button is clicked with the pointer (the way of the input handler, with its pause on the target).
	res, err := r.act(t, protocol.A11yActionReq{ID: "a2", Generation: g, Op: "click"})
	if err != nil || res.Method != "pointer" || r.in.without() != "move 730,310; down 1; up 1" {
		t.Errorf("%+v %v %v", res, err, r.in.dev.ops)
	}
	r.in.dev.ops = nil
	// A menu item, by its own action.
	r2 := newA11yRig()
	r2.bus.nodes[ref("button")].role = atMenuItem
	g2 := r2.tree(t, protocol.A11yTreeReq{}).Generation
	res, err = r2.act(t, protocol.A11yActionReq{ID: "a2", Generation: g2, Op: "click"})
	if err != nil || res.Method != "action:click" || len(r2.in.dev.ops) != 0 {
		t.Errorf("%+v %v %v", res, err, r2.in.dev.ops)
	}
	res, err = r.act(t, protocol.A11yActionReq{ID: "a3", Generation: g, Op: "click"})
	if err != nil || res.Method != "pointer" {
		t.Fatalf("%+v %v", res, err)
	}
	if got := r.in.without(); got != "move 740,410; down 1; up 1" {
		t.Errorf("the click went through the input handler's way of clicking: %s", got)
	}
	if r.in.dev.ops[len(r.in.dev.ops)-1] != "sync" {
		t.Errorf("synced: %v", r.in.dev.ops)
	}
}

func TestA11ySetTextAndTheKeyboardFallback(t *testing.T) {
	r := newA11yRig()
	g := r.tree(t, protocol.A11yTreeReq{}).Generation
	res, err := r.act(t, protocol.A11yActionReq{ID: "a4", Generation: g, Op: "set_text", Text: "héllo"})
	if err != nil || res.Method != "editable_text" || !slices.Equal(r.bus.setTexts, []string{"héllo"}) {
		t.Fatalf("%+v %v %v", res, err, r.bus.setTexts)
	}

	// A field that does not keep what it is given is typed into.
	r2 := newA11yRig()
	r2.h = NewA11y(&stubbornBus{r2.bus}, r2.d, r2.in.in)
	g2 := r2.tree(t, protocol.A11yTreeReq{}).Generation
	res, err = r2.act(t, protocol.A11yActionReq{ID: "a4", Generation: g2, Op: "set_text", Text: "abc"})
	if err != nil || res.Method != "keyboard" {
		t.Fatalf("%+v %v", res, err)
	}
	if got := r2.in.without(); got != "keys ctrl+a x1; type abc" {
		t.Errorf("select all, then type: %s", got)
	}
}

// stubbornBus says yes to SetText and keeps the old text.
type stubbornBus struct{ *atBus }

func (s *stubbornBus) SetText(context.Context, a11y.Ref, string) (bool, error) { return true, nil }

func TestA11yNeverEchoesTheTextItWasToType(t *testing.T) {
	const secret = "s3cr3t-passphrase"
	r := newA11yRig()
	g := r.tree(t, protocol.A11yTreeReq{}).Generation
	// A button is not a field: the refusal names the element, it does not repeat the text.
	_, err := r.act(t, protocol.A11yActionReq{ID: "a2", Generation: g, Op: "set_text", Text: secret})
	if code(err) != protocol.CodeInvalidArgs || strings.Contains(err.Error(), secret) {
		t.Errorf("%v", err)
	}

	// And when the keyboard fails.
	r2 := newA11yRig()
	r2.h = NewA11y(&stubbornBus{r2.bus}, r2.d, r2.in.in)
	g2 := r2.tree(t, protocol.A11yTreeReq{}).Generation
	r2.in.dev.failOn = "type " + secret
	_, err = r2.act(t, protocol.A11yActionReq{ID: "a4", Generation: g2, Op: "set_text", Text: secret})
	if code(err) != protocol.CodeInternal || strings.Contains(err.Error(), secret) {
		t.Errorf("a failing keyboard is an internal error that does not repeat the text: %v", err)
	}
}

func TestA11yDriverRefusesWhatInputWouldRefuse(t *testing.T) {
	r := newA11yRig()
	d := a11yDriver{in: r.in.in}
	ctx := context.Background()
	if err := d.Click(ctx, 5000, 5); err == nil {
		t.Error("a click outside the screen")
	}
	if err := d.Keys(ctx, "not a key"); err == nil {
		t.Error("an unknown key")
	}
	if err := d.Type(ctx, "bell\a"); err == nil {
		t.Error("a control character")
	}
	if len(r.in.dev.ops) != 0 {
		t.Errorf("a refused event did something: %v", r.in.dev.ops)
	}
	r.in.dev.sizeErr = errors.New("no screen")
	if err := d.Click(ctx, 5, 5); err == nil {
		t.Error("no screen size")
	}
	r.in.dev.sizeErr = nil
	r.in.dev.failOn, r.in.dev.failErr = "sync", errors.New("BadRequest")
	if err := d.Click(ctx, 5, 5); err == nil {
		t.Error("a sync that fails")
	}
}

func TestA11yDesktopAdapter(t *testing.T) {
	if w, err := (a11yDesktop{}).ActiveWindow(context.Background()); w != nil || err != nil {
		t.Errorf("no desktop, no window: %v %v", w, err)
	}
	d := twoWindows()
	w, err := (a11yDesktop{d}).ActiveWindow(context.Background())
	if err != nil || w == nil || w.PID != 904 || w.Title != "Terminal - cs@controlsnack: ~" {
		t.Errorf("%+v %v", w, err)
	}
	d.active = nil
	if w, err := (a11yDesktop{d}).ActiveWindow(context.Background()); w != nil || err != nil {
		t.Errorf("%v %v", w, err)
	}
}

func TestFitNodesKeepsTheStartThatFits(t *testing.T) {
	big := strings.Repeat("<", 1000) // JSON writes each of these as six bytes
	var nodes []protocol.A11yNode
	for i := range 400 {
		nodes = append(nodes, protocol.A11yNode{ID: "a" + string(rune('0'+i%10)), Role: "text", Name: big, Value: big})
	}
	kept, cut := fitNodes(nodes, maxTreeBytes)
	if !cut || len(kept) == 0 || len(kept) >= len(nodes) {
		t.Fatalf("kept %d of %d, cut=%v", len(kept), len(nodes), cut)
	}
	b, _ := json.Marshal(protocol.A11yTreeRes{Generation: "g1", Nodes: kept})
	if len(b) > protocol.MaxHeaderBytes {
		t.Errorf("the answer is %d bytes, over the header limit %d", len(b), protocol.MaxHeaderBytes)
	}
	if kept2, cut2 := fitNodes(nodes[:3], maxTreeBytes); cut2 || len(kept2) != 3 {
		t.Errorf("a few nodes fit: %d %v", len(kept2), cut2)
	}
	if kept3, cut3 := fitNodes(nil, 10); cut3 || len(kept3) != 0 {
		t.Errorf("%v %v", kept3, cut3)
	}
}

func TestA11yTreeBudget(t *testing.T) {
	r := newA11yRig()
	for _, ms := range []int{99, -1, 30001} {
		_, _, err := r.h.Tree(context.Background(), frame(t, protocol.TypeA11yTree, protocol.A11yTreeReq{BudgetMS: ms}))
		if code(err) != protocol.CodeInvalidArgs || !strings.Contains(err.Error(), "budget_ms") {
			t.Errorf("budget_ms %d: %v", ms, err)
		}
	}
	for _, ms := range []int{0, 100, 8000, 30000} {
		if res := r.tree(t, protocol.A11yTreeReq{BudgetMS: ms}); len(res.Nodes) != 4 {
			t.Errorf("budget_ms %d: %+v", ms, res)
		}
	}
}
