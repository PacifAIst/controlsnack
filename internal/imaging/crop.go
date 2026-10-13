// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package imaging

import (
	"image"

	xdraw "golang.org/x/image/draw"
)

// Crop returns a copy of the part of src inside r (r is clipped to the picture; the copy starts at 0,0).
func Crop(src *image.RGBA, r image.Rectangle) *image.RGBA {
	r = r.Intersect(src.Bounds())
	dst := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	for y := 0; y < r.Dy(); y++ {
		so := src.PixOffset(r.Min.X, r.Min.Y+y)
		copy(dst.Pix[y*dst.Stride:y*dst.Stride+r.Dx()*4], src.Pix[so:so+r.Dx()*4])
	}
	return dst
}

// Enlarge returns src enlarged by a whole factor with a smooth filter (Catmull-Rom keeps the edges of text sharp). A
// factor of 1 or less returns src itself. Text recognition does better on text that is twice as tall, which is what a
// screen's 12-pixel text needs.
func Enlarge(src *image.RGBA, factor int) *image.RGBA {
	if factor <= 1 {
		return src
	}
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx()*factor, b.Dy()*factor))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, b, xdraw.Src, nil)
	return dst
}
