// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e

package vm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"slices"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/imaging"
	"github.com/PacifAIst/controlsnack-local/internal/llm/actions"
	"github.com/PacifAIst/controlsnack-local/internal/llm/prompt"
	"github.com/PacifAIst/controlsnack-local/internal/llm/vision"
	"github.com/PacifAIst/controlsnack-local/internal/protocol"
)

// TestE2EVisionPipeline takes the pictures the model would be given from the real guest: scaled down, with the numbered
// boxes of the accessibility tree, and checks that where the model would click in the picture is where the element is on
// the screen.
func TestE2EVisionPipeline(t *testing.T) {
	m, _ := bootE2E(t, 1280, 800)
	ctx := context.Background()
	time.Sleep(20 * time.Second)

	var tree protocol.A11yTreeRes
	if _, err := m.Client.Call(ctx, protocol.TypeA11yTree, protocol.A11yTreeReq{Scope: "desktop", BudgetMS: 15000}, nil, &tree); err != nil {
		t.Fatal(err)
	}
	t.Logf("tree %s: %d nodes", tree.Generation, len(tree.Nodes))

	p, err := vision.NewPipeline(m.Client, vision.Config{Guest: prompt.Size{W: 1280, H: 800}, Format: imaging.WebP, MaxLongEdge: 1024, Marks: true})
	if err != nil {
		t.Fatal(err)
	}
	shot, err := p.Capture(ctx, tree.Generation)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("picture %dx%d, %d bytes, marks %q (%d boxes), cursor %v", shot.Note.W, shot.Note.H, len(shot.Image.Data), shot.Note.Marks, len(shot.Marked), shot.Cursor)
	if shot.Note.W != 1024 || shot.Note.H != 640 || shot.Mapper.GuestSize() != (prompt.Size{W: 1280, H: 800}) {
		t.Fatalf("note = %+v", shot.Note)
	}
	if len(shot.Marked) == 0 || shot.Note.Marks == "" {
		t.Fatal("the picture has no boxes although the tree has elements that can be operated")
	}
	if img, _, err := image.Decode(bytes.NewReader(shot.Image.Data)); err != nil || img.Bounds().Dx() != 1024 {
		t.Fatalf("the picture does not decode as it was described: %v", err)
	}

	checked := 0
	for _, n := range tree.Nodes {
		if !slices.Contains(shot.Marked, n.ID) || n.Bounds[2] < 8 || n.Bounds[3] < 8 {
			continue
		}
		want := shot.Mapper.Rect(n.Bounds)
		cx, cy := want[0]+want[2]/2, want[1]+want[3]/2
		set, _ := actions.NewSet(actions.ProfileFull)
		a, err := set.Parse("mouse", json.RawMessage(fmt.Sprintf(`{"op":"click","x":%d,"y":%d}`, cx, cy)))
		if err != nil {
			t.Fatal(err)
		}
		g, err := shot.Mapper.Guest(a)
		if err != nil {
			t.Fatal(err)
		}
		mouse := g.Args.(*actions.MouseArgs)
		if !image.Pt(mouse.X, mouse.Y).In(image.Rect(n.Bounds[0]-2, n.Bounds[1]-2, n.Bounds[0]+n.Bounds[2]+2, n.Bounds[1]+n.Bounds[3]+2)) {
			t.Errorf("element %s %q at %v on the screen: a click at the middle of its box in the picture (%d,%d) is at %d,%d", n.ID, n.Name, n.Bounds, cx, cy, mouse.X, mouse.Y)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no marked element was big enough to check")
	}
	t.Logf("%d marked elements checked", checked)

	// The screen has not changed, and without marks the agent does not send the picture again.
	plain, err := p.Capture(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	again, err := p.Capture(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("second plain capture reused=%v (a quiet screen: %v)", again.Reused, plain.Hash == again.Hash)
}
