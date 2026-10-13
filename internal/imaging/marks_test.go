// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package imaging

import (
	"image"
	"image/color"
	"image/draw"
	"testing"
)

func gray(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.RGBA{230, 230, 230, 255}), image.Point{}, draw.Src)
	return img
}

func at(img *image.RGBA, x, y int) color.RGBA { return img.RGBAAt(x, y) }

func TestDrawMarksDrawsAnOutlineAndALabelAndLeavesTheInsideAlone(t *testing.T) {
	src := gray(300, 200)
	out := DrawMarks(src, []Mark{{Label: "12", X: 50, Y: 40, W: 120, H: 60}})
	c := markColors[0]
	for _, p := range [][2]int{{50, 40}, {169, 40}, {50, 99}, {169, 99}, {100, 40}, {100, 41}, {100, 98}, {50, 70}, {51, 70}, {168, 70}} {
		if got := at(out, p[0], p[1]); got != c {
			t.Errorf("(%d,%d) is %v, want the outline color %v", p[0], p[1], got, c)
		}
	}
	// Inside the box, away from the label, nothing changed: the element stays visible.
	for _, p := range [][2]int{{100, 70}, {120, 80}, {160, 60}} {
		if got := at(out, p[0], p[1]); got != (color.RGBA{230, 230, 230, 255}) {
			t.Errorf("(%d,%d) inside the box was painted: %v", p[0], p[1], got)
		}
	}
	// Outside: untouched.
	for _, p := range [][2]int{{49, 40}, {170, 40}, {50, 39}, {50, 100}, {0, 0}, {299, 199}} {
		if got := at(out, p[0], p[1]); got != (color.RGBA{230, 230, 230, 255}) {
			t.Errorf("(%d,%d) outside the box was painted: %v", p[0], p[1], got)
		}
	}
	// The label has white text on the color.
	white := 0
	for y := 42; y < 58; y++ {
		for x := 52; x < 70; x++ {
			if at(out, x, y) == (color.RGBA{255, 255, 255, 255}) {
				white++
			}
		}
	}
	if white < 8 {
		t.Errorf("the label's text is not there (%d white pixels)", white)
	}
	if at(src, 50, 40) != (color.RGBA{230, 230, 230, 255}) {
		t.Error("the source picture was changed")
	}
}

func TestDrawMarksBigBoxesFirstSoSmallLabelsStayVisible(t *testing.T) {
	small := Mark{Label: "2", X: 60, Y: 60, W: 80, H: 30}
	big := Mark{Label: "1", X: 40, Y: 40, W: 200, H: 120}
	a := DrawMarks(gray(300, 200), []Mark{small, big})
	b := DrawMarks(gray(300, 200), []Mark{big, small})
	if !equalImages(a, b) {
		t.Error("the result depends on the order the marks were given in")
	}
	// The small box's top-left is its own color (the second drawn), not the big box's.
	if got := at(a, 60, 60); got != markColors[1] {
		t.Errorf("the small box is drawn on top: %v", got)
	}
	if got := at(a, 40, 40); got != markColors[0] {
		t.Errorf("the big box: %v", got)
	}
}

func equalImages(a, b *image.RGBA) bool {
	if a.Bounds() != b.Bounds() {
		return false
	}
	for i := range a.Pix {
		if a.Pix[i] != b.Pix[i] {
			return false
		}
	}
	return true
}

func TestDrawMarksLeavesOutWhatCannotBeSeen(t *testing.T) {
	src := gray(100, 100)
	for name, m := range map[string]Mark{
		"too small":     {Label: "1", X: 10, Y: 10, W: 3, H: 50},
		"too flat":      {Label: "1", X: 10, Y: 10, W: 50, H: 3},
		"off the right": {Label: "1", X: 100, Y: 10, W: 50, H: 50},
		"off the left":  {Label: "1", X: -60, Y: 10, W: 50, H: 50},
		"off the top":   {Label: "1", X: 10, Y: -60, W: 50, H: 50},
		"off below":     {Label: "1", X: 10, Y: 100, W: 50, H: 50},
		"no label":      {Label: "", X: 10, Y: 10, W: 50, H: 50},
	} {
		if out := DrawMarks(src, []Mark{m}); !equalImages(out, src) {
			t.Errorf("%s: something was drawn", name)
		}
	}
}

func TestDrawMarksClipsToThePicture(t *testing.T) {
	// Boxes that hang over the edges must not panic, and what is inside the picture is drawn.
	out := DrawMarks(gray(100, 80), []Mark{{Label: "99", X: -20, Y: -20, W: 100, H: 100}, {Label: "7", X: 60, Y: 40, W: 100, H: 100}})
	grayPx := color.RGBA{230, 230, 230, 255}
	if at(out, 78, 40) == grayPx || at(out, 40, 79) == grayPx {
		t.Error("the visible right and bottom edges of the first box are drawn")
	}
	if at(out, 61, 60) == grayPx || at(out, 80, 41) == grayPx {
		t.Error("the visible left and top edges of the second box are drawn")
	}
	if got := at(out, 30, 30); got != grayPx {
		t.Errorf("the middle of a box is not painted: %v", got)
	}
}

func TestDrawMarksPutsTheLabelOutsideABoxTooSmallForIt(t *testing.T) {
	out := DrawMarks(gray(200, 100), []Mark{{Label: "123", X: 40, Y: 50, W: 12, H: 8}})
	c := markColors[0]
	if got := at(out, 41, 45); got != c && got != (color.RGBA{255, 255, 255, 255}) {
		t.Errorf("the label sits above a tiny box: %v", got)
	}
	// And below it when the box touches the top of the picture.
	out = DrawMarks(gray(200, 100), []Mark{{Label: "123", X: 40, Y: 0, W: 12, H: 8}})
	if got := at(out, 41, 9); got != c && got != (color.RGBA{255, 255, 255, 255}) {
		t.Errorf("the label sits below a tiny box at the top: %v", got)
	}
}

func TestDrawMarksWithNoMarksIsACopy(t *testing.T) {
	src := gray(30, 20)
	out := DrawMarks(src, nil)
	if !equalImages(out, src) || out == src {
		t.Error("a copy that equals the source")
	}
}

func TestDrawMarksWorksOnAnyImageType(t *testing.T) {
	src := image.NewGray(image.Rect(0, 0, 80, 60))
	out := DrawMarks(src, []Mark{{Label: "5", X: 5, Y: 5, W: 40, H: 30}})
	if at(out, 5, 5) != markColors[0] {
		t.Errorf("%v", at(out, 5, 5))
	}
}

func TestDrawMarksColoursCycle(t *testing.T) {
	var marks []Mark
	for i := range 8 {
		marks = append(marks, Mark{Label: "1", X: i * 25, Y: 0, W: 24 - i, H: 40})
	}
	out := DrawMarks(gray(220, 50), marks)
	seen := map[color.RGBA]bool{}
	for _, m := range marks {
		seen[at(out, m.X, m.Y+20)] = true
	}
	if len(seen) != len(markColors) {
		t.Errorf("%d distinct colors among 8 boxes, want %d", len(seen), len(markColors))
	}
}
