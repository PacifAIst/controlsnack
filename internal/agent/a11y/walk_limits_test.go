// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package a11y

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"
)

// buttons builds a window with n buttons.
func buttons(n int) (*fakeBus, Ref) {
	b := newFakeBus()
	frame := b.add("frame", fakeNode{role: roleFrame, name: "Many"})
	for i := range n {
		b.child(frame, b.add(fmt.Sprintf("b%d", i), fakeNode{role: rolePushButton, name: fmt.Sprintf("Button %d", i), actions: []string{"click"}}))
	}
	return b, frame
}

func TestWalkStopsAtTheNodeLimit(t *testing.T) {
	b, frame := buttons(60)
	tree := walk(t, b, frame, Options{MaxNodes: 10})
	if len(tree.Nodes) != 10 || !tree.Truncated {
		t.Fatalf("%d nodes, truncated=%v", len(tree.Nodes), tree.Truncated)
	}
	if tree.Nodes[0].Name != "Many" || tree.Nodes[9].Name != "Button 8" {
		t.Errorf("the first nodes in document order are kept: %v", described(tree))
	}
	exact, exactFrame := buttons(9)
	if t2 := walk(t, exact, exactFrame, Options{MaxNodes: 10}); len(t2.Nodes) != 10 || t2.Truncated {
		t.Errorf("exactly the limit is not truncated: %d, %v", len(t2.Nodes), t2.Truncated)
	}
}

func TestWalkLimitsAreClamped(t *testing.T) {
	var o Options
	o.fill()
	if o.MaxNodes != DefaultMaxNodes || o.MaxDepth != DefaultMaxDepth || o.Budget != DefaultBudget || o.Concurrency != defaultConcurrency {
		t.Errorf("defaults: %+v", o)
	}
	o = Options{MaxNodes: 100000, Query: "  MiXed  "}
	o.fill()
	if o.MaxNodes != MaxMaxNodes || o.Query != "mixed" {
		t.Errorf("clamped: %+v", o)
	}
	o = Options{MaxNodes: -5, MaxDepth: -1, Budget: -time.Second}
	o.fill()
	if o.MaxNodes != DefaultMaxNodes || o.MaxDepth != DefaultMaxDepth || o.Budget != DefaultBudget {
		t.Errorf("negative values mean the default: %+v", o)
	}
}

func TestWalkDoesNotLookAtMoreThanItMust(t *testing.T) {
	// 3000 elements nobody would report: a walk for 10 nodes must not ask the bus about all of them.
	b := newFakeBus()
	frame := b.add("frame", fakeNode{role: roleFrame, name: "Haystack"})
	for i := range 3000 {
		b.child(frame, b.add(fmt.Sprintf("f%d", i), fakeNode{role: roleFiller}))
	}
	tree := walk(t, b, frame, Options{MaxNodes: 10})
	if !tree.Truncated {
		t.Error("giving up on a haystack is truncation")
	}
	if got, limit := b.infos.Load(), int64(exploreFloor); got > limit {
		t.Errorf("%d elements looked at, at most %d allowed", got, limit)
	}
}

func TestWalkStopsAtTheDepthLimit(t *testing.T) {
	b := newFakeBus()
	prev := b.add("d0", fakeNode{role: roleFrame, name: "Level 0"})
	root := prev
	for i := 1; i <= 40; i++ {
		n := b.add(fmt.Sprintf("d%d", i), fakeNode{role: rolePanel, name: fmt.Sprintf("Level %d", i)})
		b.child(prev, n)
		prev = n
	}
	tree := walk(t, b, root, Options{MaxDepth: 5})
	if len(tree.Nodes) != 6 || !tree.Truncated {
		t.Errorf("%d nodes (want levels 0 to 5), truncated=%v", len(tree.Nodes), tree.Truncated)
	}
	if deep := walk(t, b, root, Options{}); len(deep.Nodes) != DefaultMaxDepth+1 || !deep.Truncated {
		t.Errorf("default depth: %d nodes, truncated=%v", len(deep.Nodes), deep.Truncated)
	}
}

func TestWalkEndsAtTheBudgetWithWhatItFound(t *testing.T) {
	b, frame := buttons(200)
	b.latency = 20 * time.Millisecond
	start := time.Now()
	tree, err := Walk(context.Background(), b, []Ref{frame}, Options{Budget: 150 * time.Millisecond, MaxNodes: 400})
	took := time.Since(start)
	if err != nil {
		t.Fatalf("running out of time is not an error: %v", err)
	}
	if !tree.Truncated {
		t.Error("an unfinished walk must say so")
	}
	if took > time.Second {
		t.Errorf("a 150 ms budget took %v", took)
	}
	if len(tree.Nodes) == 0 || len(tree.Nodes) >= 200 {
		t.Errorf("some but not all: %d nodes", len(tree.Nodes))
	}
}

func TestWalkEndsWhenTheCallerGivesUp(t *testing.T) {
	b, frame := buttons(50)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tree, err := Walk(ctx, b, []Ref{frame}, Options{})
	if err != nil || len(tree.Nodes) != 0 || !tree.Truncated {
		t.Errorf("%d nodes, truncated=%v, err=%v", len(tree.Nodes), tree.Truncated, err)
	}
	if b.calls.Load() != 0 {
		t.Errorf("a walk that was canceled before it began made %d bus calls", b.calls.Load())
	}
}

func TestWalkNeverAsksMoreThanTheConcurrencyAtOnce(t *testing.T) {
	for _, c := range []int{1, 3, 8} {
		b, frame := buttons(40)
		b.latency = time.Millisecond
		walk(t, b, frame, Options{Concurrency: c})
		if got := b.peak.Load(); got > int64(c) {
			t.Errorf("concurrency %d: %d calls were in flight", c, got)
		}
		if c > 1 && b.peak.Load() < 2 {
			t.Errorf("concurrency %d: calls never overlapped (%d)", c, b.peak.Load())
		}
	}
}

func TestWalkQuery(t *testing.T) {
	b, frame := files()
	tests := []struct {
		query string
		want  []string
	}{
		{"", []string{"frame:Files", "button:Open", "entry:Search", "label:Hello", "button:Deep"}},
		{"open", []string{"button:Open"}},
		{"OPEN", []string{"button:Open"}},
		{"  hello ", []string{"label:Hello"}},
		{"button", []string{"button:Open", "button:Deep"}},
		{"abc", []string{"entry:Search"}}, // the value counts
		{"nothing like this", nil},
	}
	for _, tt := range tests {
		tree := walk(t, b, frame, Options{Query: tt.query})
		if got := described(tree); !slices.Equal(got, tt.want) {
			t.Errorf("query %q: got %v, want %v", tt.query, got, tt.want)
		}
	}
}

// When a limit cuts in, the same window must give the same answer every time: what is left out is decided by the
// shape of the tree, not by which call happened to come back first.
func TestWalkIsDeterministicWhenALimitCutsIn(t *testing.T) {
	var first []string
	for i := range 25 {
		b := newFakeBus()
		frame := b.add("frame", fakeNode{role: roleFrame, name: "Big"})
		for g := range 3 {
			group := b.add(fmt.Sprintf("g%d", g), fakeNode{role: rolePanel, name: fmt.Sprintf("Group %d", g)})
			b.child(frame, group)
			for j := range 30 {
				b.child(group, b.add(fmt.Sprintf("g%db%d", g, j), fakeNode{role: rolePushButton, name: fmt.Sprintf("Button %d.%d", g, j)}))
			}
		}
		b.latency, b.jitter = 50*time.Microsecond, 300*time.Microsecond
		tree := walk(t, b, frame, Options{MaxNodes: 10})
		got := described(tree)
		if len(got) != 10 || !tree.Truncated {
			t.Fatalf("run %d: %d nodes, truncated=%v", i, len(got), tree.Truncated)
		}
		if first == nil {
			first = got
			if got[0] != "frame:Big" || got[1] != "panel:Group 0" || got[2] != "button:Button 0.0" {
				t.Fatalf("document order: %v", got)
			}
		} else if !slices.Equal(got, first) {
			t.Fatalf("run %d differs:\n got %v\nwant %v", i, got, first)
		}
	}
}
