// Package thumb cuts a recording's pictures to the shapes the windows
// show them in: a card's picture (scaled to cover it and cut to its
// shape around the middle) and the trim screen's filmstrip (pictures side
// by side, the last cut at the strip's end). Pure.
package thumb

import (
	"image"

	xdraw "golang.org/x/image/draw"
)

// Cover is src scaled to cover w×h, keeping its shape, and cut to w×h
// around its middle.
func Cover(src *image.RGBA, w, h int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	if sw == 0 || sh == 0 || w == 0 || h == 0 {
		return dst
	}
	// The part of src with dst's shape: all of one side, the middle of the
	// other.
	part := sb
	if sw*h > sh*w { // wider than dst: cut the sides
		cw := sh * w / h
		part.Min.X += (sw - cw) / 2
		part.Max.X = part.Min.X + cw
	} else { // taller: cut the top and bottom
		ch := sw * h / w
		part.Min.Y += (sh - ch) / 2
		part.Max.Y = part.Min.Y + ch
	}
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, part, xdraw.Src, nil)
	return dst
}

// Strip is a filmstrip width×tileH of tiles (pictures evenly spaced
// through a recording, in order) each covering tileW×tileH: each place on
// the strip shows the picture nearest its time, and the last is cut at
// the strip's end. A nil tile (not made yet) leaves its place clear.
func Strip(tiles []*image.RGBA, tileW, tileH, width int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, width, tileH))
	if len(tiles) == 0 || tileW <= 0 || tileH <= 0 || width <= 0 {
		return dst
	}
	covers := map[int]*image.RGBA{}
	for x := 0; x < width; x += tileW {
		mid := float64(x) + float64(tileW)/2
		k := min(len(tiles)-1, int(mid/float64(width)*float64(len(tiles))))
		if tiles[k] == nil {
			continue
		}
		c, ok := covers[k]
		if !ok {
			c = Cover(tiles[k], tileW, tileH)
			covers[k] = c
		}
		xdraw.Copy(dst, image.Pt(x, 0), c, c.Bounds(), xdraw.Src, nil)
	}
	return dst
}
