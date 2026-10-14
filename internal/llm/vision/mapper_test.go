// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package vision

import (
	"errors"
	"math/rand"
	"strings"
	"testing"

	"github.com/PacifAIst/controlsnack-local/internal/llm/prompt"
	"github.com/PacifAIst/controlsnack-local/internal/protocol"
)

func mapper(t testing.TB, gw, gh, iw, ih int) Mapper {
	t.Helper()
	m, err := NewMapper(prompt.Size{W: gw, H: gh}, prompt.Size{W: iw, H: ih})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestAMapperNeedsRealSizes(t *testing.T) {
	for _, tc := range [][4]int{{0, 800, 100, 100}, {1280, 0, 100, 100}, {1280, 800, 0, 1}, {1280, 800, 1, -1}, {9000, 800, 100, 100}, {1280, 800, 100, 9000}} {
		if _, err := NewMapper(prompt.Size{W: tc[0], H: tc[1]}, prompt.Size{W: tc[2], H: tc[3]}); err == nil {
			t.Errorf("%v: expected an error", tc)
		}
	}
	m, err := Identity(prompt.Size{W: 1280, H: 800})
	if err != nil || !m.IsIdentity() || m.GuestSize() != m.ImageSize() {
		t.Fatalf("Identity = %+v %v", m, err)
	}
	if mapper(t, 1920, 1080, 1280, 720).IsIdentity() {
		t.Error("a scaled mapper is not the identity")
	}
	if g := mapper(t, 1920, 1080, 1280, 720); g.GuestSize() != (prompt.Size{W: 1920, H: 1080}) || g.ImageSize() != (prompt.Size{W: 1280, H: 720}) {
		t.Errorf("sizes: %+v %+v", g.GuestSize(), g.ImageSize())
	}
}

func TestScaleRoundsHalfAwayFromZero(t *testing.T) {
	for _, tc := range []struct{ v, to, from, want int }{
		{0, 5, 3, 0}, {1, 2, 3, 1}, {3, 2, 3, 2}, {5, 1, 2, 3}, {7, 1, 2, 4}, {-5, 1, 2, -3}, {-1, 2, 3, -1}, {1279, 1920, 1280, 1919}, {1000000, 3, 4, 750000},
	} {
		if got := scale(tc.v, tc.to, tc.from); got != tc.want {
			t.Errorf("scale(%d, %d, %d) = %d, want %d", tc.v, tc.to, tc.from, got, tc.want)
		}
	}
}

var screens = []struct{ gw, gh, iw, ih int }{
	{1280, 800, 1280, 800}, {1280, 800, 1024, 640}, {1920, 1080, 1280, 720}, {1366, 768, 1024, 576}, {1920, 1200, 768, 480}, {800, 600, 300, 225}, {1280, 800, 1280, 800},
}

func TestPointsGoThereAndBack(t *testing.T) {
	for _, s := range screens {
		m := mapper(t, s.gw, s.gh, s.iw, s.ih)
		step := s.gw/s.iw + 1 // one picture pixel is this many screen pixels at most
		prev := -1
		for x := 0; x < s.gw; x++ {
			p := m.Point(x, 0)
			if p.X < 0 || p.X > s.iw || p.X < prev {
				t.Fatalf("%v: x=%d goes to %d (previous %d)", s, x, p.X, prev)
			}
			prev = p.X
			back, err := m.GuestX("x", min(p.X, s.iw-1))
			if err != nil || abs(back-x) > step {
				t.Fatalf("%v: x=%d goes to %d and back to %d (%v)", s, x, p.X, back, err)
			}
		}
		for y := 0; y < s.gh; y++ {
			p := m.Point(0, y)
			back, err := m.GuestY("y", min(p.Y, s.ih-1))
			if err != nil || abs(back-y) > s.gh/s.ih+1 {
				t.Fatalf("%v: y=%d goes to %d and back to %d (%v)", s, y, p.Y, back, err)
			}
		}
	}
}

func TestEveryPixelOfThePictureIsAPixelOfTheScreen(t *testing.T) {
	for _, s := range screens {
		m := mapper(t, s.gw, s.gh, s.iw, s.ih)
		for x := 0; x < s.iw; x++ {
			g, err := m.GuestX("x", x)
			if err != nil || g < 0 || g >= s.gw {
				t.Fatalf("%v: picture x=%d is screen x=%d (%v)", s, x, g, err)
			}
		}
		for y := 0; y < s.ih; y++ {
			g, err := m.GuestY("y", y)
			if err != nil || g < 0 || g >= s.gh {
				t.Fatalf("%v: picture y=%d is screen y=%d (%v)", s, y, g, err)
			}
		}
	}
}

func TestRectsThatTouchStillTouch(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	for _, s := range screens {
		m := mapper(t, s.gw, s.gh, s.iw, s.ih)
		for range 500 {
			x := rng.Intn(s.gw - 2)
			w1 := 1 + rng.Intn(s.gw-x-1)
			w2 := 1 + rng.Intn(s.gw-x-w1+1)
			a := m.Rect(protocol.Rect{x, 0, w1, 10})
			b := m.Rect(protocol.Rect{x + w1, 0, w2, 10})
			// A rectangle that would vanish keeps one pixel, which may overlap its neighbor by that pixel.
			if gap := a[0] + a[2] - b[0]; gap < 0 || gap > 1 {
				t.Fatalf("%v: [%d+%d] and [%d] do not touch after scaling: %v %v", s, x, w1, x+w1, a, b)
			}
			if a[2] < 1 || b[2] < 1 {
				t.Fatalf("%v: a rectangle that is not empty became empty: %v %v", s, a, b)
			}
		}
	}
	m := mapper(t, 1280, 800, 640, 400)
	if r := m.Rect(protocol.Rect{10, 10, 0, 0}); r[2] != 0 || r[3] != 0 {
		t.Errorf("an empty rectangle stays empty: %v", r)
	}
	if r := m.Rect(protocol.Rect{1, 1, 1, 1}); r[2] != 1 || r[3] != 1 {
		t.Errorf("a pixel stays a pixel: %v", r)
	}
	if r := mapper(t, 1280, 800, 1280, 800).Rect(protocol.Rect{3, 4, 5, 6}); r != (prompt.Rect{3, 4, 5, 6}) {
		t.Errorf("the identity changes a rectangle: %v", r)
	}
}

func TestTheCursorStaysOnThePicture(t *testing.T) {
	m := mapper(t, 1920, 1080, 1280, 720)
	for _, tc := range []struct {
		in   protocol.Point
		want prompt.Point
	}{{protocol.Point{X: 960, Y: 540}, prompt.Point{X: 640, Y: 360}}, {protocol.Point{X: 1919, Y: 1079}, prompt.Point{X: 1279, Y: 719}}, {protocol.Point{X: 5000, Y: -4}, prompt.Point{X: 1279, Y: 0}}} {
		if got := m.Cursor(tc.in); got != tc.want {
			t.Errorf("Cursor(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestACoordinateAlittleBeyondTheEdgeIsTheEdge(t *testing.T) {
	m := mapper(t, 1920, 1200, 1280, 800) // 2 % of 1280 is 25, of 800 is 16
	for _, tc := range []struct {
		x       int
		want    int
		wantErr bool
	}{
		{0, 0, false}, {640, 960, false}, {1279, 1919, false},
		{1280, 1919, false}, {1304, 1919, false}, {1305, 0, true},
		{-1, 0, false}, {-25, 0, false}, {-26, 0, true}, {100000, 0, true},
	} {
		got, err := m.GuestX("x", tc.x)
		var re *RangeError
		if tc.wantErr {
			if !errors.As(err, &re) || re.Value != tc.x || re.Max != 1279 || re.Size != 1280 {
				t.Errorf("x=%d: err = %v", tc.x, err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("x=%d: got %d, %v; want %d", tc.x, got, err, tc.want)
		}
	}
	for _, tc := range []struct {
		y       int
		wantErr bool
	}{{799, false}, {815, false}, {816, true}, {-16, false}, {-17, true}} {
		_, err := m.GuestY("y", tc.y)
		if (err != nil) != tc.wantErr {
			t.Errorf("y=%d: err = %v", tc.y, err)
		}
	}
	_, err := m.GuestX("to_x", 2000)
	if err == nil || !strings.Contains(err.Error(), "to_x=2000 is outside the screenshot, which is 1280 pixels wide (use 0 to 1279)") {
		t.Errorf("the message says what is valid: %v", err)
	}
	if _, err := m.GuestY("y", 2000); err == nil || !strings.Contains(err.Error(), "800 pixels high (use 0 to 799)") {
		t.Errorf("vertical message: %v", err)
	}
	// A tiny picture has no overshoot to give.
	small := mapper(t, 400, 300, 40, 30)
	if _, err := small.GuestX("x", 40); err == nil {
		t.Error("2 % of 40 pixels is not even one pixel")
	}
}

func TestRegions(t *testing.T) {
	m := mapper(t, 1920, 1200, 1280, 800)
	for name, tc := range map[string]struct {
		in   [4]int
		want [4]int
		bad  string
	}{
		"inside":           {[4]int{100, 100, 200, 100}, [4]int{150, 150, 300, 150}, ""},
		"whole picture":    {[4]int{0, 0, 1280, 800}, [4]int{0, 0, 1920, 1200}, ""},
		"to the far edge":  {[4]int{1180, 700, 100, 100}, [4]int{1770, 1050, 150, 150}, ""},
		"a little over":    {[4]int{1200, 700, 100, 110}, [4]int{1800, 1050, 120, 150}, ""},
		"starts before":    {[4]int{-10, -5, 110, 105}, [4]int{0, 0, 150, 150}, ""},
		"one pixel":        {[4]int{10, 10, 1, 1}, [4]int{15, 15, 2, 2}, ""},
		"no width":         {[4]int{10, 10, 0, 5}, [4]int{}, "at least 1"},
		"negative height":  {[4]int{10, 10, 5, -5}, [4]int{}, "at least 1"},
		"starts too far":   {[4]int{1400, 0, 10, 10}, [4]int{}, "region.x=1400"},
		"ends too far":     {[4]int{1000, 0, 400, 10}, [4]int{}, "region.x+width=1400"},
		"below the screen": {[4]int{0, 790, 10, 100}, [4]int{}, "region.y+height=890"},
	} {
		t.Run(name, func(t *testing.T) {
			got, errs := m.GuestRect(tc.in)
			if tc.bad != "" {
				if len(errs) == 0 || !strings.Contains(errs[0].Error(), tc.bad) {
					t.Fatalf("errs = %v, want one with %q", errs, tc.bad)
				}
				return
			}
			if len(errs) != 0 || got != tc.want {
				t.Fatalf("got %v %v, want %v", got, errs, tc.want)
			}
		})
	}
	// Whatever is asked, a region that is accepted is a real, non-empty part of the screen.
	rng := rand.New(rand.NewSource(5))
	for range 2000 {
		in := [4]int{rng.Intn(1400) - 60, rng.Intn(900) - 50, rng.Intn(1400) - 10, rng.Intn(900) - 10}
		got, errs := m.GuestRect(in)
		if len(errs) > 0 {
			continue
		}
		if got[0] < 0 || got[1] < 0 || got[2] < 1 || got[3] < 1 || got[0]+got[2] > 1920 || got[1]+got[3] > 1200 {
			t.Fatalf("%v became %v, which is not a part of a 1920x1200 screen", in, got)
		}
	}
}

func TestAnOvershootIsTheEdgePixelNotWhateverItScalesTo(t *testing.T) {
	// The picture is half the screen, so the last pixel of the picture is screen pixel 1278 and one past it would be 1280.
	m := mapper(t, 1280, 800, 640, 400)
	for _, x := range []int{639, 640, 641, 650} {
		if got, err := m.GuestX("x", x); err != nil || got != 1278 {
			t.Errorf("x=%d -> %d, %v; the edge of the picture is screen pixel 1278", x, got, err)
		}
	}
	g, errs := m.GuestRect([4]int{600, 0, 50, 10})
	if len(errs) != 0 || g != [4]int{1200, 0, 80, 20} {
		t.Errorf("a region that ends a little past the picture ends at the edge of the screen: %v %v", g, errs)
	}
}

func TestAPointIsAlwaysOnTheScreenEvenWhenThePictureIsTheBiggerOne(t *testing.T) {
	// A picture is never larger than the screen, but a Mapper does not rely on it: the last pixel of a 1000-pixel picture of a
	// 100-pixel screen rounds to 100, which is not on the screen.
	m := mapper(t, 100, 100, 1000, 1000)
	if x, err := m.GuestX("x", 999); err != nil || x != 99 {
		t.Errorf("x -> %d, %v", x, err)
	}
	if y, err := m.GuestY("y", 999); err != nil || y != 99 {
		t.Errorf("y -> %d, %v", y, err)
	}
}
