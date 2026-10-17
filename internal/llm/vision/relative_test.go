// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package vision

import (
	"context"
	"testing"

	"github.com/PacifAIst/controlsnack-local/internal/config"
	"github.com/PacifAIst/controlsnack-local/internal/llm/actions"
	"github.com/PacifAIst/controlsnack-local/internal/llm/detect"
	"github.com/PacifAIst/controlsnack-local/internal/llm/prompt"
	"github.com/PacifAIst/controlsnack-local/internal/protocol"
)

func TestPlanCarriesHowTheModelPoints(t *testing.T) {
	llm := config.Defaults().LLM
	base := detect.Capabilities{Vision: true, NativeTools: true}
	for name, tc := range map[string]struct {
		coords   string
		want     string
		relative bool
		space    prompt.Size
	}{
		"pixels":        {detect.CoordsPixels, detect.CoordsPixels, false, prompt.Size{W: 1024, H: 640}},
		"not found out": {"", detect.CoordsPixels, false, prompt.Size{W: 1024, H: 640}},
		"relative":      {detect.CoordsRelative, detect.CoordsRelative, true, prompt.Size{W: 1000, H: 1000}},
	} {
		t.Run(name, func(t *testing.T) {
			caps := base
			caps.Coords = tc.coords
			s, err := Plan(caps, true, llm)
			if err != nil {
				t.Fatal(err)
			}
			if s.Coords != tc.want || s.Relative() != tc.relative || s.CoordSpace(prompt.Size{W: 1024, H: 640}) != tc.space {
				t.Errorf("setup = %+v relative %v space %v", s, s.Relative(), s.CoordSpace(prompt.Size{W: 1024, H: 640}))
			}
		})
	}
	// A model that does not see has no picture to point at, whatever a cache of an earlier probe says.
	text := Setup{Coords: detect.CoordsRelative}
	if text.Relative() || text.CoordSpace(prompt.Size{W: 800, H: 600}) != (prompt.Size{W: 800, H: 600}) {
		t.Error("text-only mode is never relative")
	}
	caps := detect.Capabilities{NativeTools: true, Coords: detect.CoordsRelative}
	if s, _ := Plan(caps, true, llm); s.Coords != "" {
		t.Errorf("text-only setup has coords %q", s.Coords)
	}
}

// The pipeline of a model that points on the scale of 0 to 1000: the picture is as large as ever, the coordinates are not.
func TestAPipelineOfARelativeModel(t *testing.T) {
	a := newAgent(1920, 1080, 2.0/3)
	p := newPipeline(t, a, func(c *Config) { c.CoordSpace = prompt.Size{W: 1000, H: 1000} })
	shot, err := p.Capture(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if shot.Note.W != 1280 || shot.Note.H != 720 {
		t.Errorf("the picture keeps its size: %+v", shot.Note)
	}
	if shot.Mapper.ImageSize() != (prompt.Size{W: 1000, H: 1000}) || shot.Mapper.GuestSize() != (prompt.Size{W: 1920, H: 1080}) {
		t.Fatalf("mapper = %+v %+v", shot.Mapper.ImageSize(), shot.Mapper.GuestSize())
	}
	// The cursor in the middle of the screen is in the middle of the scale.
	if shot.Cursor != (prompt.Point{X: 500, Y: 500}) {
		t.Errorf("cursor = %v", shot.Cursor)
	}
	// What the model reads goes to the scale, and what it says comes back from it.
	if r := shot.Mapper.Rect(protocol.Rect{960, 540, 192, 108}); r != (prompt.Rect{500, 500, 100, 100}) {
		t.Errorf("rect = %v", r)
	}
	for _, tc := range []struct{ x, y, gx, gy int }{{0, 0, 0, 0}, {500, 500, 960, 540}, {999, 999, 1918, 1078}} {
		a := parseAction(t, "mouse", `{"op":"click","x":`+itoa(tc.x)+`,"y":`+itoa(tc.y)+`}`)
		g, err := shot.Mapper.Guest(a)
		if err != nil {
			t.Fatal(err)
		}
		if m := g.Args.(*actions.MouseArgs); abs(m.X-tc.gx) > 2 || abs(m.Y-tc.gy) > 2 {
			t.Errorf("(%d,%d) of 1000 became screen (%d,%d), want about (%d,%d)", tc.x, tc.y, m.X, m.Y, tc.gx, tc.gy)
		}
	}
	// 1001 is an overshoot of the scale, not of the picture.
	if _, err := shot.Mapper.Guest(parseAction(t, "mouse", `{"op":"click","x":1100,"y":5}`)); err == nil {
		t.Error("1100 is outside the scale of 0 to 1000")
	}
}

// The observer of a relative model tells it positions on the scale and the picture's description in pixels.
func TestAnObserverOfARelativeModel(t *testing.T) {
	g := newGuest(1920, 1080, 2.0/3)
	setup := visionSetup()
	setup.Coords = detect.CoordsRelative
	seen, err := observer(t, g, setup, nil).Look(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var obs prompt.Observation
	seen.Fill(&obs)
	if obs.Screen != (prompt.Size{W: 1000, H: 1000}) || obs.Image == nil || obs.Image.W != 1280 || obs.Image.H != 720 {
		t.Fatalf("screen %v image %+v", obs.Screen, obs.Image)
	}
	// The button is at (960, 540) of a 1920x1080 screen, which is (500, 500) on the scale.
	button := obs.UI.Nodes[1]
	if button.Bounds != (prompt.Rect{500, 500, 52, 37}) {
		t.Errorf("button = %v", button.Bounds)
	}
	if obs.Cursor.X != 500 || obs.Cursor.Y != 500 { // the pointer, from the screenshot: the middle of the screen
		t.Errorf("cursor = %v", *obs.Cursor)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}
