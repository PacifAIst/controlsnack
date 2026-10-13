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

func TestWalkSurvivesBrokenElements(t *testing.T) {
	b, frame := files()
	panel := b.nodes[frame].children[0]
	b.child(panel, b.add("broken-info", fakeNode{role: rolePushButton, name: "Broken", infoErr: errBoom}))
	b.child(panel, ref("never-existed"))
	badKids := b.add("broken-children", fakeNode{role: rolePushButton, name: "Bad child list", childErr: errBoom})
	b.child(panel, badKids)
	b.child(panel, b.add("after", fakeNode{role: rolePushButton, name: "After the broken ones"}))

	got := described(walk(t, b, frame, Options{}))
	for _, want := range []string{"frame:Files", "button:Open", "button:Deep", "button:Bad child list", "button:After the broken ones"} {
		if !slices.Contains(got, want) {
			t.Errorf("%q is missing from %v", want, got)
		}
	}
	if slices.Contains(got, "button:Broken") {
		t.Errorf("an element that cannot be read is not reported: %v", got)
	}
}

func TestWalkStopsAtACycle(t *testing.T) {
	b := newFakeBus()
	frame := b.add("frame", fakeNode{role: roleFrame, name: "Loop"})
	button := b.add("button", fakeNode{role: rolePushButton, name: "Once"})
	b.child(frame, button)
	b.child(button, frame) // a child that is its own ancestor
	b.child(button, button)
	b.child(frame, button) // reachable twice
	tree := walk(t, b, frame, Options{})
	if want := []string{"frame:Loop", "button:Once"}; !slices.Equal(described(tree), want) {
		t.Errorf("got %v, want %v", described(tree), want)
	}
}

func TestWalkKeepsDocumentOrderWhateverTheTimings(t *testing.T) {
	build := func() (*fakeBus, Ref) {
		b := newFakeBus()
		frame := b.add("frame", fakeNode{role: roleFrame, name: "Order"})
		for i := range 6 {
			group := b.add(fmt.Sprintf("g%d", i), fakeNode{role: rolePanel, name: fmt.Sprintf("Group %d", i)})
			b.child(frame, group)
			for j := range 4 {
				b.child(group, b.add(fmt.Sprintf("g%db%d", i, j), fakeNode{role: rolePushButton, name: fmt.Sprintf("Button %d.%d", i, j)}))
			}
		}
		return b, frame
	}
	fast, root := build()
	want := described(walk(t, fast, root, Options{}))
	if len(want) != 1+6+24 {
		t.Fatalf("%d nodes: %v", len(want), want)
	}
	if want[1] != "panel:Group 0" || want[2] != "button:Button 0.0" || want[6] != "panel:Group 1" {
		t.Errorf("a parent comes before its children, siblings in order: %v", want[:8])
	}
	for range 5 {
		slow, root := build()
		slow.latency, slow.jitter = 100*time.Microsecond, 400*time.Microsecond
		if got := described(walk(t, slow, root, Options{})); !slices.Equal(got, want) {
			t.Fatalf("the order changed with the timing:\n got %v\nwant %v", got, want)
		}
	}
}

func TestWalkNeedsNoNodesFromAnEmptyRootList(t *testing.T) {
	tree, err := Walk(context.Background(), newFakeBus(), nil, Options{})
	if err != nil || len(tree.Nodes) != 0 || tree.Truncated {
		t.Errorf("%+v %v", tree, err)
	}
}
