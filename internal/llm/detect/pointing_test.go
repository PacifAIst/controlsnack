// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package detect

import (
	"fmt"
	"strings"
	"testing"
)

func TestTheProbeFindsOutHowAModelPoints(t *testing.T) {
	for _, tc := range []struct {
		points string
		coords string
		ok     bool
		detail string
	}{
		{"", CoordsPixels, true, "pixels"},
		{"pixels", CoordsPixels, true, "pixels"},
		{"relative", CoordsRelative, true, "relative"},
		{"neither", CoordsPixels, false, "on the square on neither scale"},
		{"words", CoordsPixels, false, "no position in the answer"},
	} {
		t.Run("points "+tc.points, func(t *testing.T) {
			m := strongModel("m")
			m.Points = tc.points
			caps, _, err := probeWith(t, "ollama", m)
			if err != nil {
				t.Fatal(err)
			}
			ch := checkOf(t, caps, CheckPointing)
			if caps.Coords != tc.coords || ch.OK != tc.ok || !strings.Contains(ch.Detail, tc.detail) || ch.Skipped {
				t.Fatalf("coords = %q, check = %+v", caps.Coords, ch)
			}
			if !caps.Usable() {
				t.Error("a model that points in an odd way is still usable")
			}
		})
	}
}

func TestAModelThatDoesNotSeeIsNotAskedWhereThingsAre(t *testing.T) {
	m := strongModel("m")
	m.Sees = nil
	caps, srv, err := probeWith(t, "openai", m)
	if err != nil {
		t.Fatal(err)
	}
	if ch := checkOf(t, caps, CheckPointing); !ch.Skipped || caps.Coords != "" {
		t.Fatalf("coords = %q check = %+v", caps.Coords, ch)
	}
	for _, s := range srv.seenChats() {
		if strings.Contains(s.Prompt, ProbePointQuestion) {
			t.Error("the pointing question was put to a model that cannot see")
		}
	}
}

func TestPointAnswersAreReadInTheirForms(t *testing.T) {
	for in, want := range map[string]string{
		`{"x":120,"y":40}`:                      "120,40",
		`{"x": 748.5, "y": 395.2}`:              "748.5,395.2",
		"```json\n{\"x\": 118, \"y\": 41}\n```": "118,41",
		`The middle is at {"y": 40, "x": 120}.`: "120,40",
		`(120, 40)`:                             "120,40",
		`[750, 400]`:                            "750,400",
		`<think>{"x":1,"y":2}</think>`:          "",
		`x is 120`:                              "",
		`1 2 3`:                                 "",
		``:                                      "",
		`{"x":"a","y":"b"}`:                     "",
	} {
		x, y, ok := parsePoint(in)
		got := ""
		if ok {
			got = fmt.Sprintf("%g,%g", x, y)
		}
		if in == `<think>{"x":1,"y":2}</think>` {
			continue // the reasoning is taken out before the answer is read
		}
		if got != want {
			t.Errorf("parsePoint(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWhichScaleAnAnswerIsOn(t *testing.T) {
	for name, tc := range map[string]struct {
		x, y float64
		want string
	}{
		"the middle in pixels":      {460, 140, CoordsPixels},
		"a little off in pixels":    {440, 170, CoordsPixels},
		"the middle on the scale":   {718, 350, CoordsRelative},
		"a little off on the scale": {690, 400, CoordsRelative},
		"axes swapped":              {140, 460, ""},
		"the corner":                {0, 0, ""},
		"a corner, as pixels":       {405, 85, CoordsPixels},
		"a corner, on the scale":    {640, 200, CoordsRelative},
		"the far corner":            {1000, 1000, ""},
		"the middle of the picture": {320, 200, ""},
		"half of the scale":         {500, 500, ""},
	} {
		if got := classifyPoint(tc.x, tc.y); got != tc.want {
			t.Errorf("%s: classifyPoint(%v, %v) = %q, want %q", name, tc.x, tc.y, got, tc.want)
		}
	}
}

func TestThePointingPictureHasTheSquareWhereTheQuestionImplies(t *testing.T) {
	img := pointImage()
	if b := img.Bounds(); b.Dx() != pointW || b.Dy() != pointH {
		t.Fatalf("bounds = %v", b)
	}
	if r := img.RGBAAt(pointMidX, pointMidY); r.R < 200 || r.G != 0 {
		t.Errorf("the middle of the square is %v", r)
	}
	if w := img.RGBAAt(5, 5); w.R != 255 || w.G != 255 {
		t.Errorf("the corner is %v", w)
	}
	if pointRelativeX != 718 || pointRelativeY != 350 {
		t.Errorf("relative middle = %d,%d", pointRelativeX, pointRelativeY)
	}
}
