// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package imaging

import (
	"image"
	"image/color"
	"image/draw"
	"sort"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// Mark is a numbered box drawn around an element on the picture the model sees (the set-of-marks overlay, D-30). Small
// local models find a number on a picture far more reliably than they estimate a coordinate. X, Y, W and H are in the
// pixels of the picture the mark is drawn on.
type Mark struct {
	Label      string
	X, Y, W, H int
}

// markColors are dark enough for white text and far enough apart that neighbors differ.
var markColors = []color.RGBA{
	{200, 30, 30, 255}, {0, 105, 200, 255}, {20, 130, 55, 255}, {160, 55, 190, 255}, {195, 95, 0, 255}, {0, 120, 120, 255},
}

const (
	markBorder = 2
	// minMarkSize is the smallest box worth drawing: below it the outline would cover the element.
	minMarkSize = 4
)

// Drawn returns the marks that DrawMarks draws on a picture of the given bounds, in the order it draws them (the biggest
// first): the ones that lie inside the picture, are big enough and have a label. The caller that has to tell the model
// which elements carry a box asks the same question DrawMarks does.
func Drawn(b image.Rectangle, marks []Mark) []Mark {
	ordered := make([]Mark, 0, len(marks))
	for _, m := range marks {
		if m.W >= minMarkSize && m.H >= minMarkSize && m.X < b.Dx() && m.Y < b.Dy() && m.X+m.W > 0 && m.Y+m.H > 0 && m.Label != "" {
			ordered = append(ordered, m)
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].W*ordered[i].H > ordered[j].W*ordered[j].H })
	return ordered
}

// DrawMarks returns a copy of img with the marks drawn on it. The first argument is never changed (it may be the screen
// the viewer shows). Bigger boxes are drawn first, so the label of a small element inside a big one stays visible;
// boxes that lie outside the picture or are too small are left out. Drawing is clipped to the picture.
func DrawMarks(img image.Image, marks []Mark) *image.RGBA {
	b := img.Bounds()
	out := image.NewRGBA(b)
	draw.Draw(out, b, img, b.Min, draw.Src)

	ordered := Drawn(b, marks)

	face := basicfont.Face7x13
	for i, m := range ordered {
		c := markColors[i%len(markColors)]
		r := image.Rect(m.X, m.Y, m.X+m.W, m.Y+m.H).Add(b.Min)
		outline(out, r, c)

		tw := font.MeasureString(face, m.Label).Ceil()
		lw, lh := tw+4, face.Metrics().Height.Ceil()+2
		// The label sits inside the top-left corner; on a box too small to hold it, above the box (or below, at the top
		// of the picture).
		lx, ly := r.Min.X+markBorder, r.Min.Y+markBorder
		if m.W < lw+2*markBorder || m.H < lh+2*markBorder {
			lx, ly = r.Min.X, r.Min.Y-lh
			if ly < b.Min.Y {
				ly = r.Max.Y
			}
		}
		box := image.Rect(lx, ly, lx+lw, ly+lh)
		draw.Draw(out, box.Intersect(b), image.NewUniform(c), image.Point{}, draw.Src)
		d := font.Drawer{Dst: out, Src: image.White, Face: face, Dot: fixed.P(lx+2, ly+lh-3)}
		d.DrawString(m.Label)
	}
	return out
}

// outline draws a border of markBorder pixels just inside r, clipped to the picture.
func outline(dst *image.RGBA, r image.Rectangle, c color.RGBA) {
	bounds := dst.Bounds()
	fill := func(x0, y0, x1, y1 int) {
		draw.Draw(dst, image.Rect(x0, y0, x1, y1).Intersect(bounds), image.NewUniform(c), image.Point{}, draw.Src)
	}
	t := markBorder
	fill(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+t)
	fill(r.Min.X, r.Max.Y-t, r.Max.X, r.Max.Y)
	fill(r.Min.X, r.Min.Y, r.Min.X+t, r.Max.Y)
	fill(r.Max.X-t, r.Min.Y, r.Max.X, r.Max.Y)
}
