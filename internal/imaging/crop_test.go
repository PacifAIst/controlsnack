// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package imaging

import (
	"image"
	"image/color"
	"testing"
)

func pattern(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetRGBA(x, y, color.RGBA{uint8(x), uint8(y), uint8(x ^ y), 255})
		}
	}
	return img
}

func TestCrop(t *testing.T) {
	src := pattern(40, 30)
	got := Crop(src, image.Rect(10, 5, 25, 20))
	if got.Bounds() != image.Rect(0, 0, 15, 15) {
		t.Fatalf("%v", got.Bounds())
	}
	for _, p := range [][2]int{{0, 0}, {14, 14}, {7, 3}} {
		if got.RGBAAt(p[0], p[1]) != src.RGBAAt(p[0]+10, p[1]+5) {
			t.Errorf("pixel %v", p)
		}
	}
	// A copy: changing it does not change the source.
	got.SetRGBA(0, 0, color.RGBA{1, 2, 3, 255})
	if src.RGBAAt(10, 5) == got.RGBAAt(0, 0) {
		t.Error("the crop shares pixels with the source")
	}
}

func TestCropIsClippedToThePicture(t *testing.T) {
	src := pattern(40, 30)
	got := Crop(src, image.Rect(30, 20, 100, 100))
	if got.Bounds() != image.Rect(0, 0, 10, 10) || got.RGBAAt(9, 9) != src.RGBAAt(39, 29) {
		t.Errorf("%v", got.Bounds())
	}
	if got := Crop(src, image.Rect(50, 50, 60, 60)); got.Bounds().Dx() != 0 || got.Bounds().Dy() != 0 {
		t.Errorf("outside the picture: %v", got.Bounds())
	}
	sub := src.SubImage(image.Rect(5, 5, 20, 20)).(*image.RGBA)
	if got := Crop(sub, image.Rect(6, 6, 8, 8)); got.RGBAAt(0, 0) != src.RGBAAt(6, 6) {
		t.Error("a crop of a sub-image uses the sub-image's own coordinates")
	}
}

func TestEnlarge(t *testing.T) {
	src := pattern(8, 6)
	if Enlarge(src, 1) != src || Enlarge(src, 0) != src || Enlarge(src, -3) != src {
		t.Error("a factor of 1 or less changes nothing")
	}
	got := Enlarge(src, 3)
	if got.Bounds() != image.Rect(0, 0, 24, 18) {
		t.Fatalf("%v", got.Bounds())
	}
	flat := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for i := range flat.Pix {
		flat.Pix[i] = 200
	}
	for _, c := range Enlarge(flat, 2).Pix {
		if c < 199 || c > 201 {
			t.Fatalf("a flat picture stays flat: %d", c)
		}
	}
}
