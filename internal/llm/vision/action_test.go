// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package vision

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/PacifAIst/controlsnack/internal/llm/actions"
	"github.com/PacifAIst/controlsnack/internal/llm/prompt"
	"github.com/PacifAIst/controlsnack/internal/protocol"
)

func parseAction(t testing.TB, tool, args string) actions.Action {
	t.Helper()
	set, err := actions.NewSet(actions.ProfileFull)
	if err != nil {
		t.Fatal(err)
	}
	a, err := set.Parse(tool, json.RawMessage(args))
	if err != nil {
		t.Fatalf("%s %s: %v", tool, args, err)
	}
	return a
}

func TestGuestMapsTheCoordinatesOfMouseScrollAndRegions(t *testing.T) {
	m := mapper(t, 1920, 1200, 1280, 800)

	mouse := parseAction(t, "mouse", `{"op":"click","x":640,"y":400}`)
	g, err := m.Guest(mouse)
	if err != nil {
		t.Fatal(err)
	}
	if a := g.Args.(*actions.MouseArgs); a.X != 960 || a.Y != 600 || a.ToX != nil || a.Op != "click" {
		t.Errorf("mouse = %+v", a)
	}
	if a := mouse.Args.(*actions.MouseArgs); a.X != 640 || a.Y != 400 {
		t.Errorf("the action given was changed: %+v", a)
	}
	if g.Tool != "mouse" || string(g.Raw) != string(mouse.Raw) {
		t.Errorf("the tool and the raw arguments (what the model said) are kept: %+v", g)
	}

	drag := parseAction(t, "mouse", `{"op":"drag","x":100,"y":100,"to_x":200,"to_y":300}`)
	if g, err = m.Guest(drag); err != nil {
		t.Fatal(err)
	}
	if a := g.Args.(*actions.MouseArgs); a.X != 150 || a.Y != 150 || *a.ToX != 300 || *a.ToY != 450 {
		t.Errorf("drag = %+v to %d,%d", a, *a.ToX, *a.ToY)
	}
	if a := drag.Args.(*actions.MouseArgs); *a.ToX != 200 {
		t.Error("the end of the drag was changed in the action given")
	}

	scroll := parseAction(t, "scroll", `{"x":1280,"y":400,"dy":3}`)
	if g, err = m.Guest(scroll); err != nil {
		t.Fatal(err)
	}
	if a := g.Args.(*actions.ScrollArgs); a.X != 1919 || a.Y != 600 || a.DY != 3 {
		t.Errorf("scroll = %+v (the edge, from a position just past it)", a)
	}

	region := parseAction(t, "read_screen_text", `{"region":[100,100,200,100]}`)
	if g, err = m.Guest(region); err != nil {
		t.Fatal(err)
	}
	if a := g.Args.(*actions.ReadScreenTextArgs); *a.Region != [4]int{150, 150, 300, 150} {
		t.Errorf("region = %v", *a.Region)
	}
	if a := region.Args.(*actions.ReadScreenTextArgs); *a.Region != [4]int{100, 100, 200, 100} {
		t.Errorf("the region given was changed: %v", *a.Region)
	}
}

func TestOtherActionsPassThroughUntouched(t *testing.T) {
	m := mapper(t, 1920, 1200, 1280, 800)
	for tool, args := range map[string]string{
		"exec": `{"command":"ls"}`, "key": `{"keys":"ctrl+l"}`, "click_element": `{"id":"a17"}`, "read_screen_text": `{}`, "done": `{"success":true,"summary":"ok"}`, "screenshot": `{}`,
	} {
		a := parseAction(t, tool, args)
		g, err := m.Guest(a)
		if err != nil || g.Args != a.Args {
			t.Errorf("%s: %+v %v", tool, g, err)
		}
	}
}

func TestACoordinateOutsideThePictureIsAProblemTheModelCanReadAndFix(t *testing.T) {
	m := mapper(t, 1920, 1200, 1280, 800)
	drag := parseAction(t, "mouse", `{"op":"drag","x":2000,"y":10,"to_x":5,"to_y":900}`)
	g, err := m.Guest(drag)
	var ae *actions.Error
	if !errors.As(err, &ae) || ae.Code != actions.CodeInvalidArgs || ae.Tool != "mouse" {
		t.Fatalf("err = %v", err)
	}
	if len(ae.Problems) != 2 || ae.Problems[0].Path != "args.x" || ae.Problems[1].Path != "args.to_y" {
		t.Fatalf("a problem for each coordinate that is out: %+v", ae.Problems)
	}
	for _, want := range []string{"x=2000 is outside the screenshot", "1280 pixels wide", "to_y=900", "800 pixels high"} {
		if !strings.Contains(ae.Error(), want) {
			t.Errorf("the error lacks %q: %v", want, ae)
		}
	}
	if g.Args != drag.Args {
		t.Error("an action that is refused is returned as it was")
	}

	bad := parseAction(t, "read_screen_text", `{"region":[0,0,5000,10]}`)
	if _, err = m.Guest(bad); !errors.As(err, &ae) || ae.Problems[0].Path != "args.region" || !strings.Contains(ae.Error(), "region.x+width=5000") {
		t.Errorf("region: %v", err)
	}
}

func TestInTextOnlyModeCoordinatesAreScreenPixelsButStillChecked(t *testing.T) {
	m, err := Identity(prompt.Size{W: 1280, H: 800})
	if err != nil {
		t.Fatal(err)
	}
	g, err := m.Guest(parseAction(t, "mouse", `{"op":"click","x":1279,"y":799}`))
	if err != nil || g.Args.(*actions.MouseArgs).X != 1279 {
		t.Fatalf("%+v %v", g, err)
	}
	if _, err = m.Guest(parseAction(t, "mouse", `{"op":"click","x":1400,"y":5}`)); err == nil {
		t.Error("a click off the screen")
	}
}

func TestConversionsToThePicture(t *testing.T) {
	m := mapper(t, 1920, 1080, 1280, 720)
	w := m.Window(&protocol.Window{ID: "0x1", Title: "Terminal", Class: "xfce4-terminal", Bounds: protocol.Rect{0, 0, 1920, 1040}})
	if w == nil || w.Class != "xfce4-terminal" || w.Title != "Terminal" || w.Bounds != (prompt.Rect{0, 0, 1280, 693}) {
		t.Errorf("window = %+v", w)
	}
	if m.Window(nil) != nil {
		t.Error("no window stays none")
	}
	tree := m.UITree(protocol.A11yTreeRes{Generation: "g4", Truncated: true, Note: "partial", Nodes: []protocol.A11yNode{
		{ID: "a1", Role: "push button", Name: "OK", Description: "d", Value: "v", States: []string{"focused"}, Actions: []string{"click"}, Bounds: protocol.Rect{960, 540, 300, 150}},
	}}, "desktop")
	if tree.Generation != "g4" || tree.Scope != "desktop" || !tree.Truncated || tree.Note != "partial" || len(tree.Nodes) != 1 {
		t.Fatalf("tree = %+v", tree)
	}
	n := tree.Nodes[0]
	if n.ID != "a1" || n.Role != "push button" || n.Name != "OK" || n.Description != "d" || n.Value != "v" || n.States[0] != "focused" || n.Actions[0] != "click" || n.Bounds != (prompt.Rect{640, 360, 200, 100}) {
		t.Errorf("node = %+v", n)
	}
	ocr := m.OCR(protocol.OCRRes{Generation: "g2", Truncated: true, Lines: []protocol.OCRLine{{ID: "o1", Text: "Hello", Conf: 91, Bounds: protocol.Rect{30, 60, 300, 30}}}})
	if ocr.Generation != "g2" || !ocr.Truncated || len(ocr.Lines) != 1 || ocr.Lines[0].Bounds != (prompt.Rect{20, 40, 200, 20}) || ocr.Lines[0].Text != "Hello" {
		t.Errorf("ocr = %+v", ocr)
	}
	if got := m.Nodes(nil); len(got) != 0 {
		t.Errorf("nodes = %v", got)
	}
}

func TestEachCoordinateThatIsOutIsReportedByItself(t *testing.T) {
	m := mapper(t, 1920, 1200, 1280, 800)
	for name, tc := range map[string]struct {
		tool, args string
		paths      []string
	}{
		"only y":        {"mouse", `{"op":"click","x":5,"y":2000}`, []string{"args.y"}},
		"only x":        {"mouse", `{"op":"click","x":2000,"y":5}`, []string{"args.x"}},
		"only to_x":     {"mouse", `{"op":"drag","x":5,"y":5,"to_x":3000,"to_y":5}`, []string{"args.to_x"}},
		"scroll":        {"scroll", `{"x":2000,"y":2000,"dy":1}`, []string{"args.x", "args.y"}},
		"scroll only y": {"scroll", `{"x":5,"y":2000,"dy":1}`, []string{"args.y"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := m.Guest(parseAction(t, tc.tool, tc.args))
			var ae *actions.Error
			if !errors.As(err, &ae) {
				t.Fatalf("err = %v", err)
			}
			var got []string
			for _, p := range ae.Problems {
				got = append(got, p.Path)
			}
			if strings.Join(got, ",") != strings.Join(tc.paths, ",") {
				t.Errorf("problems at %v, want %v", got, tc.paths)
			}
		})
	}
}
