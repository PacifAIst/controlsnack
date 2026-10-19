// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package vision

import (
	"fmt"

	"github.com/PacifAIst/controlsnack/internal/llm/prompt"
	"github.com/PacifAIst/controlsnack/internal/protocol"
)

// maxScreen bounds a size this package accepts; the guest screen is at most 1920x1080 (the protocol allows 7680x4320).
const maxScreen = 8192

// overshootPercent is how far past the edge of the picture a coordinate may be and still be taken as the edge: a model
// that aims at the corner of the screen says 1281 on a picture 1280 pixels wide. Further than that it is a mistake it
// is told about.
const overshootPercent = 2

// Mapper converts between the pixels of the guest screen and the pixels of the picture the model is given, which is the
// screen scaled to fit llm.image_max (PLAN.md section 9.3). What the model reads (bounds, cursor) goes from the guest to
// the picture, what it says (mouse, scroll, regions) from the picture back to the guest.
type Mapper struct {
	gw, gh, iw, ih int
}

// NewMapper makes the mapper of a picture of size image taken of a screen of size guest.
func NewMapper(guest, image prompt.Size) (Mapper, error) {
	for _, s := range []prompt.Size{guest, image} {
		if s.W < 1 || s.H < 1 || s.W > maxScreen || s.H > maxScreen {
			return Mapper{}, fmt.Errorf("a size of %dx%d pixels is not one a screen or a picture has", s.W, s.H)
		}
	}
	return Mapper{gw: guest.W, gh: guest.H, iw: image.W, ih: image.H}, nil
}

// Identity is the mapper of a model that is given no picture (text-only mode): it says guest pixels and reads guest
// pixels. The coordinates it says are still checked against the screen.
func Identity(guest prompt.Size) (Mapper, error) { return NewMapper(guest, guest) }

// GuestSize is the size of the guest screen; ImageSize the size of the picture.
func (m Mapper) GuestSize() prompt.Size { return prompt.Size{W: m.gw, H: m.gh} }

// ImageSize is the size of the picture the model sees.
func (m Mapper) ImageSize() prompt.Size { return prompt.Size{W: m.iw, H: m.ih} }

// IsIdentity reports whether a picture pixel is a screen pixel.
func (m Mapper) IsIdentity() bool { return m.gw == m.iw && m.gh == m.ih }

// scale is round(v * to / from) for any sign of v, with integers so that the result does not depend on the platform.
func scale(v, to, from int) int {
	n := int64(v) * int64(to)
	d := int64(from)
	if n >= 0 {
		return int((2*n + d) / (2 * d))
	}
	return -int((2*(-n) + d) / (2 * d))
}

// Point converts a point of the guest screen to the picture.
func (m Mapper) Point(x, y int) prompt.Point {
	return prompt.Point{X: scale(x, m.iw, m.gw), Y: scale(y, m.ih, m.gh)}
}

// Cursor converts the pointer position and keeps it on the picture.
func (m Mapper) Cursor(p protocol.Point) prompt.Point {
	pt := m.Point(p.X, p.Y)
	return prompt.Point{X: min(max(pt.X, 0), m.iw-1), Y: min(max(pt.Y, 0), m.ih-1)}
}

// Rect converts a rectangle of the guest screen to the picture. The corners are converted and the size is what lies
// between them, so that two elements that touch on the screen still touch on the picture; a rectangle that is not empty
// does not become empty.
func (m Mapper) Rect(r protocol.Rect) prompt.Rect {
	x0, y0 := scale(r[0], m.iw, m.gw), scale(r[1], m.ih, m.gh)
	x1, y1 := scale(r[0]+r[2], m.iw, m.gw), scale(r[1]+r[3], m.ih, m.gh)
	w, h := x1-x0, y1-y0
	if r[2] > 0 {
		w = max(w, 1)
	}
	if r[3] > 0 {
		h = max(h, 1)
	}
	return prompt.Rect{x0, y0, w, h}
}

// RangeError is a coordinate the model said that is outside the picture by more than the allowed overshoot. Its words say
// what is valid, for the model to read.
type RangeError struct {
	// Name is the argument ("x", "to_y", "region.x").
	Name string
	// Value is what the model said; Max the largest valid value (the last pixel, or the size for the far edge of a region).
	Value, Max int
	// Axis is "wide" or "high": which size of the picture the value is measured against.
	Axis string
	Size int
}

func (e *RangeError) Error() string {
	return fmt.Sprintf("%s=%d is outside the screenshot, which is %d pixels %s (use 0 to %d)", e.Name, e.Value, e.Size, e.Axis, e.Max)
}

// edge converts a coordinate of the picture to the screen. last is the largest valid value of the axis (the size minus
// one for a point, the size for the far side of a region); a value up to overshootPercent of the size beyond either end is
// the end.
func edge(name string, v, last, size, to, from int, axis string) (int, error) {
	tol := size * overshootPercent / 100
	switch {
	case v < -tol || v > last+tol:
		return 0, &RangeError{Name: name, Value: v, Max: last, Axis: axis, Size: size}
	case v < 0:
		v = 0
	case v > last:
		v = last
	}
	return scale(v, to, from), nil
}

// GuestX converts a horizontal position the model gave, in pixels of the picture, to the screen. A position a little
// beyond the picture is taken as its edge; one further out is a *RangeError named name.
func (m Mapper) GuestX(name string, x int) (int, error) {
	g, err := edge(name, x, m.iw-1, m.iw, m.gw, m.iw, "wide")
	return min(g, m.gw-1), err
}

// GuestY is GuestX for a vertical position.
func (m Mapper) GuestY(name string, y int) (int, error) {
	g, err := edge(name, y, m.ih-1, m.ih, m.gh, m.ih, "high")
	return min(g, m.gh-1), err
}

// GuestRect converts a region the model gave, [x, y, width, height] in pixels of the picture, to the screen. The region
// is clipped to the picture (within the overshoot) and is never empty.
func (m Mapper) GuestRect(r [4]int) (g [4]int, errs []error) {
	if r[2] < 1 || r[3] < 1 {
		return g, []error{fmt.Errorf("region has a width of %d and a height of %d, both must be at least 1", r[2], r[3])}
	}
	x0, e0 := edge("region.x", r[0], m.iw-1, m.iw, m.gw, m.iw, "wide")
	y0, e1 := edge("region.y", r[1], m.ih-1, m.ih, m.gh, m.ih, "high")
	// The far corner is the first pixel outside the region, so it may be the size itself.
	x1, e2 := edge("region.x+width", r[0]+r[2], m.iw, m.iw, m.gw, m.iw, "wide")
	y1, e3 := edge("region.y+height", r[1]+r[3], m.ih, m.ih, m.gh, m.ih, "high")
	for _, e := range []error{e0, e1, e2, e3} {
		if e != nil {
			errs = append(errs, e)
		}
	}
	if len(errs) > 0 {
		return g, errs
	}
	x0, y0 = min(x0, m.gw-1), min(y0, m.gh-1)
	x1, y1 = max(x1, x0+1), max(y1, y0+1)
	return [4]int{x0, y0, x1 - x0, y1 - y0}, nil
}
