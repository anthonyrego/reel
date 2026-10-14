// Package thumb cuts pictures to the shapes the windows show them in: a
// card's picture (scaled to cover it and cut to its shape around the
// middle), the trim screen's filmstrip (pictures side by side, the last cut
// at the strip's end) and the camera bubble's frames. Pure.
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

// Bubble cuts camera frames to the camera bubble: the middle square of
// each, mirrored (people expect to see themselves as in a mirror) and
// scaled to the bubble's pixels. It keeps its buffers from frame to frame:
// one per camera, used by one goroutine at a time.
type Bubble struct {
	half   []byte  // the square halved while it's twice the bubble or more, BGRA
	cols   []int32 // per bubble column, mirrored: the left tap's byte offset
	colsW  []int32 // and the right tap's weight, of 256
	rows   []int32 // per bubble row: the top tap's row
	rowsW  []int32 // and the bottom tap's weight, of 256
	scaled [2]int  // the square and bubble sizes cols and rows are for
}

// Cut fills dst (square) with the middle square of pix, a w×h BGRA frame
// stride bytes a row, mirrored and scaled to dst. Shrinking halves the
// square 2×2 at a time while it's twice dst or more, then interpolates
// bilinearly, so it neither aliases nor costs a filter's taps: a camera
// sends 30 frames a second. A flat color comes out exactly.
func (b *Bubble) Cut(dst *image.RGBA, pix []byte, w, h, stride int) {
	d := dst.Bounds().Dx()
	s := min(w, h)
	if d <= 0 || s < 2 || stride < w*4 || len(pix) < (h-1)*stride+w*4 {
		return
	}
	src, sstride := pix[(h-s)/2*stride+(w-s)/2*4:], stride
	for halved := false; s >= 2*d && s >= 2; halved = true {
		n := s / 2
		if !halved && cap(b.half) < n*n*4 {
			b.half = make([]byte, n*n*4)
		}
		// Halving again works within half: row j is written from rows 2j
		// and 2j+1, at or after it, so writes trail reads.
		half := b.half[:n*n*4]
		for j := range n {
			r0, r1 := src[2*j*sstride:][:8*n], src[(2*j+1)*sstride:][:8*n]
			o := half[j*n*4:][:4*n]
			for i := 0; i < 4*n; i += 4 {
				p, q := r0[2*i:2*i+8], r1[2*i:2*i+8]
				o[i] = uint8((uint32(p[0]) + uint32(p[4]) + uint32(q[0]) + uint32(q[4]) + 2) >> 2)
				o[i+1] = uint8((uint32(p[1]) + uint32(p[5]) + uint32(q[1]) + uint32(q[5]) + 2) >> 2)
				o[i+2] = uint8((uint32(p[2]) + uint32(p[6]) + uint32(q[2]) + uint32(q[6]) + 2) >> 2)
				o[i+3] = uint8((uint32(p[3]) + uint32(p[7]) + uint32(q[3]) + uint32(q[7]) + 2) >> 2)
			}
		}
		src, sstride, s = half, n*4, n
	}
	dh := min(d, dst.Bounds().Dy())
	if s == d {
		// The same size: a mirrored copy, BGRA to RGBA.
		for y := range dh {
			r := src[y*sstride:][:4*d]
			o := dst.Pix[dst.PixOffset(dst.Rect.Min.X, dst.Rect.Min.Y+y):][:4*d]
			for x, at := 0, 4*(d-1); x < 4*d; x, at = x+4, at-4 {
				p := r[at : at+4]
				o[x], o[x+1], o[x+2], o[x+3] = p[2], p[1], p[0], 0xff
			}
		}
		return
	}
	b.taps(s, d)
	for y := range dh {
		r0 := src[int(b.rows[y])*sstride:][:4*s]
		r1 := r0
		if int(b.rows[y])+1 < s {
			r1 = src[(int(b.rows[y])+1)*sstride:][:4*s]
		}
		fy := uint32(b.rowsW[y])
		gy := 256 - fy
		o := dst.Pix[dst.PixOffset(dst.Rect.Min.X, dst.Rect.Min.Y+y):][:4*d]
		for x := range d {
			at, fx := int(b.cols[x]), uint32(b.colsW[x])
			gx := 256 - fx
			p, q := r0[at:at+8], r1[at:at+8]
			mix := func(c int) uint8 {
				top := uint32(p[c])*gx + uint32(p[4+c])*fx
				bot := uint32(q[c])*gx + uint32(q[4+c])*fx
				return uint8((top*gy + bot*fy + 1<<15) >> 16)
			}
			// BGRA in, RGBA out.
			o[4*x], o[4*x+1], o[4*x+2], o[4*x+3] = mix(2), mix(1), mix(0), 0xff
		}
	}
}

// taps works out, once per size, where each bubble pixel samples an s×s
// square scaled to d×d, mirrored across.
func (b *Bubble) taps(s, d int) {
	if b.scaled == [2]int{s, d} {
		return
	}
	b.scaled = [2]int{s, d}
	b.cols, b.colsW = make([]int32, d), make([]int32, d)
	b.rows, b.rowsW = make([]int32, d), make([]int32, d)
	for i := range d {
		u := (float64(i)+0.5)*float64(s)/float64(d) - 0.5
		u = max(0, min(float64(s-1), u))
		// The right (or lower) tap is always inside: the last pixel is
		// the one before it, weighted fully.
		at := min(int(u), s-2)
		wt := int32((u - float64(at)) * 256)
		b.rows[i], b.rowsW[i] = int32(at), wt
		// Mirrored: column i of the bubble is column d-1-i of the frame.
		b.cols[d-1-i], b.colsW[d-1-i] = int32(at*4), wt
	}
}
