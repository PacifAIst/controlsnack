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
	"strings"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack/internal/imaging"
	"github.com/PacifAIst/controlsnack/internal/llm/actions"
	"github.com/PacifAIst/controlsnack/internal/llm/backend"
	"github.com/PacifAIst/controlsnack/internal/llm/prompt"
	"github.com/PacifAIst/controlsnack/internal/llm/vision"
	"github.com/PacifAIst/controlsnack/internal/protocol"
	"github.com/PacifAIst/controlsnack/internal/safety/untrusted"
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

// TestE2ETextOnlyObserver looks at the real guest the way a model without vision does: no picture, the accessibility tree
// and the text recognized on the screen, rendered as the observation the model would read.
func TestE2ETextOnlyObserver(t *testing.T) {
	m, _ := bootE2E(t, 1280, 800)
	ctx := context.Background()
	time.Sleep(20 * time.Second)

	var res protocol.ExecRes
	cmd := `(setsid xfce4-terminal --geometry=70x12 -e "bash -c 'clear; echo TEXT ONLY MARKER 4711; echo Second line: Hello World; sleep 900'" >/dev/null 2>&1 &); echo launched`
	if _, err := m.Client.Call(ctx, protocol.TypeExec, protocol.ExecReq{Command: cmd}, nil, &res); err != nil || res.Stdout != "launched\n" {
		t.Fatalf("%+v %v", res, err)
	}
	deadline := time.Now().Add(3 * time.Minute)
	for {
		var ws protocol.WindowsRes
		if _, err := m.Client.Call(ctx, protocol.TypeWindows, protocol.WindowsReq{Op: "list"}, nil, &ws); err != nil {
			t.Fatal(err)
		}
		if slices.ContainsFunc(ws.Windows, func(w protocol.Window) bool { return strings.EqualFold(w.Class, "Xfce4-terminal") }) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the terminal did not open")
		}
		time.Sleep(5 * time.Second)
	}
	time.Sleep(8 * time.Second)

	setup := vision.Setup{Mode: backend.ModeJSON, Profile: "compact"}
	o, err := vision.NewObserver(m.Client, vision.ObserverConfig{Setup: setup, Guest: prompt.Size{W: 1280, H: 800}, TreeBudget: 20 * time.Second, OCRBudget: 90 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	begin := time.Now()
	seen, err := o.Look(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("looked in %s: %d tree nodes, %d lines of text, notes %v", time.Since(begin).Round(time.Millisecond), len(seen.UI.Nodes), len(seen.OCR.Lines), seen.Notes)
	if seen.Shot != nil || seen.OCR == nil || seen.UI == nil || seen.Active == nil {
		t.Fatalf("seen = %+v", seen)
	}

	obs := prompt.Observation{Turn: 1, Network: "disabled", Task: "read the terminal", Budget: prompt.Budget{ActionsLeft: 50, TurnsLeft: 30, TimeLeft: 20 * time.Minute}}
	seen.Fill(&obs)
	text := prompt.Renderer{Fencer: untrustedFencer(t), Format: prompt.FormatText}.Observation(obs, prompt.Limits{})
	t.Logf("what the model reads:\n%s", text)
	var found bool
	for _, l := range seen.OCR.Lines {
		if strings.Contains(strings.ToLower(l.Text), "4711") {
			found = true
		}
	}
	if !found {
		t.Errorf("the marker text was not recognized in %d lines", len(seen.OCR.Lines))
	}
	if strings.Contains(text, "[image attached") || !strings.Contains(text, "mode: text-only") || !strings.Contains(text, "OCR generation") {
		t.Errorf("observation:\n%s", text)
	}
}

func untrustedFencer(t *testing.T) *untrusted.Fencer {
	t.Helper()
	f, err := untrusted.New()
	if err != nil {
		t.Fatal(err)
	}
	return f
}
