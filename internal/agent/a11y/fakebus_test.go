// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package a11y

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"
)

// Role numbers used by the tests (AtspiRole, atspi-constants.h).
const (
	roleCheckBox   = 7
	roleFiller     = 20
	roleFrame      = 23
	roleLabel      = 29
	roleTableCell  = 56
	roleListItem   = 32
	roleMenuItem   = 35
	rolePanel      = 39
	rolePassword   = 40
	rolePushButton = 43
	roleSeparator  = 50
	roleSlider     = 51
	roleApp        = 75
	roleEntry      = 79
)

// shown is the state of an element that is on screen and enabled.
const shown = StateSet(1<<StateShowing | 1<<StateVisible | 1<<StateEnabled | 1<<StateSensitive)

// fakeNode is one element of the in-memory tree.
type fakeNode struct {
	role     uint32
	state    StateSet
	name     string
	desc     string
	children []Ref
	x, y     int
	w, h     int
	noExtent bool // no Component interface
	actions  []string
	text     string
	noText   bool
	number   *float64
	infoErr  error
	childErr error
}

// fakeBus is an accessibility bus with a tree in memory.
type fakeBus struct {
	mu    sync.Mutex
	nodes map[Ref]*fakeNode
	apps  []App

	latency time.Duration
	jitter  time.Duration
	calls   atomic.Int64
	infos   atomic.Int64
	texts   atomic.Int64

	inflight atomic.Int64
	peak     atomic.Int64

	// what actions did
	done     []string
	focused  []Ref
	setTexts map[Ref]string
	actOK    bool // DoAction / GrabFocus / SetText report success

	blockAction bool // DoAction does not return until its context ends
	ignoreSet   bool // SetText reports success without changing the text
}

func newFakeBus() *fakeBus {
	return &fakeBus{nodes: map[Ref]*fakeNode{}, setTexts: map[Ref]string{}, actOK: true}
}

func ref(path string) Ref { return Ref{Bus: ":1.7", Path: "/org/a11y/atspi/accessible/" + path} }

// add registers a node and returns its reference.
func (b *fakeBus) add(path string, n fakeNode) Ref {
	r := ref(path)
	if n.state == 0 {
		n.state = shown
	}
	if n.w == 0 && n.h == 0 && !n.noExtent {
		n.w, n.h = 80, 24
	}
	b.nodes[r] = &n
	return r
}

// child links a child below a parent.
func (b *fakeBus) child(parent, child Ref) {
	b.nodes[parent].children = append(b.nodes[parent].children, child)
}

func (b *fakeBus) enter(ctx context.Context) (func(), error) {
	b.calls.Add(1)
	n := b.inflight.Add(1)
	for {
		p := b.peak.Load()
		if n <= p || b.peak.CompareAndSwap(p, n) {
			break
		}
	}
	d := b.latency
	if b.jitter > 0 {
		d += time.Duration(rand.Int63n(int64(b.jitter)))
	}
	if d > 0 {
		select {
		case <-time.After(d):
		case <-ctx.Done():
			b.inflight.Add(-1)
			return nil, ctx.Err()
		}
	}
	return func() { b.inflight.Add(-1) }, nil
}

func (b *fakeBus) node(r Ref) (*fakeNode, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n, ok := b.nodes[r]
	if !ok {
		return nil, ErrGone
	}
	return n, nil
}

func (b *fakeBus) Apps(ctx context.Context) ([]App, error) { return b.apps, nil }

func (b *fakeBus) Info(ctx context.Context, r Ref) (Info, error) {
	done, err := b.enter(ctx)
	if err != nil {
		return Info{}, err
	}
	defer done()
	b.infos.Add(1)
	n, err := b.node(r)
	if err != nil {
		return Info{}, err
	}
	if n.infoErr != nil {
		return Info{}, n.infoErr
	}
	return Info{Role: n.role, State: n.state, Name: n.name, Description: n.desc, ChildCount: len(n.children)}, nil
}

func (b *fakeBus) Children(ctx context.Context, r Ref) ([]Ref, error) {
	done, err := b.enter(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	n, err := b.node(r)
	if err != nil {
		return nil, err
	}
	if n.childErr != nil {
		return nil, n.childErr
	}
	return append([]Ref(nil), n.children...), nil
}

func (b *fakeBus) Extents(ctx context.Context, r Ref) (int, int, int, int, error) {
	done, err := b.enter(ctx)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	defer done()
	n, err := b.node(r)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	if n.noExtent {
		return 0, 0, 0, 0, ErrUnsupported
	}
	return n.x, n.y, n.w, n.h, nil
}

func (b *fakeBus) Actions(ctx context.Context, r Ref) ([]string, error) {
	done, err := b.enter(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	n, err := b.node(r)
	if err != nil {
		return nil, err
	}
	return n.actions, nil
}

func (b *fakeBus) Text(ctx context.Context, r Ref, max int) (string, error) {
	done, err := b.enter(ctx)
	if err != nil {
		return "", err
	}
	defer done()
	b.texts.Add(1)
	n, err := b.node(r)
	if err != nil {
		return "", err
	}
	if n.noText {
		return "", ErrUnsupported
	}
	t := []rune(n.text)
	if len(t) > max {
		t = t[:max]
	}
	return string(t), nil
}

func (b *fakeBus) Number(ctx context.Context, r Ref) (float64, error) {
	done, err := b.enter(ctx)
	if err != nil {
		return 0, err
	}
	defer done()
	n, err := b.node(r)
	if err != nil {
		return 0, err
	}
	if n.number == nil {
		return 0, ErrUnsupported
	}
	return *n.number, nil
}

func (b *fakeBus) DoAction(ctx context.Context, r Ref, index int) (bool, error) {
	done, err := b.enter(ctx)
	if err != nil {
		return false, err
	}
	defer done()
	n, err := b.node(r)
	if err != nil {
		return false, err
	}
	if index < 0 || index >= len(n.actions) {
		return false, ErrUnsupported
	}
	b.mu.Lock()
	b.done = append(b.done, fmt.Sprintf("%s#%d:%s", r.Path, index, n.actions[index]))
	b.mu.Unlock()
	if b.blockAction { // a button that opened a dialog and waits in it
		<-ctx.Done()
		return false, ctx.Err()
	}
	return b.actOK, nil
}

func (b *fakeBus) GrabFocus(ctx context.Context, r Ref) (bool, error) {
	done, err := b.enter(ctx)
	if err != nil {
		return false, err
	}
	defer done()
	if _, err := b.node(r); err != nil {
		return false, err
	}
	b.mu.Lock()
	b.focused = append(b.focused, r)
	b.mu.Unlock()
	return b.actOK, nil
}

func (b *fakeBus) SetText(ctx context.Context, r Ref, text string) (bool, error) {
	done, err := b.enter(ctx)
	if err != nil {
		return false, err
	}
	defer done()
	n, err := b.node(r)
	if err != nil {
		return false, err
	}
	if n.noText || !n.state.Has(StateEditable) {
		return false, ErrUnsupported
	}
	b.mu.Lock()
	b.setTexts[r] = text
	if !b.ignoreSet { // a field that says yes and keeps what it had
		n.text = text
	}
	b.mu.Unlock()
	return b.actOK, nil
}

func (b *fakeBus) Close() error { return nil }

var errBoom = errors.New("boom")
