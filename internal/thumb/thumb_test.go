package thumb

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"testing"
)

var (
	red  = color.RGBA{0xff, 0, 0, 0xff}
	blue = color.RGBA{0, 0, 0xff, 0xff}
)

// halves is w×h, red in its first half and blue in its second, across
// (wide) or down (tall).
func halves(w, h int, across bool) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			c := red
			if (across && x >= w/2) || (!across && y >= h/2) {
				c = blue
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

func solid(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, c.A
	}
	return img
}

func near(a, b color.RGBA) bool {
	d := func(x, y uint8) int {
		if x > y {
			return int(x - y)
		}
		return int(y - x)
	}
	return d(a.R, b.R) < 8 && d(a.G, b.G) < 8 && d(a.B, b.B) < 8 && d(a.A, b.A) < 8
}

// A wide picture loses its sides, a tall one its top and bottom: the
// middle stays.
func TestCover(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  *image.RGBA
		w, h int
		at   [2]image.Point // where red and blue land
	}{
		{"wide", halves(400, 100, true), 100, 100, [2]image.Point{{10, 50}, {90, 50}}},
		{"tall", halves(100, 400, false), 100, 100, [2]image.Point{{50, 10}, {50, 90}}},
		{"16:10 from 16:9", halves(1600, 900, true), 160, 100, [2]image.Point{{20, 50}, {140, 50}}},
	} {
		got := Cover(tc.src, tc.w, tc.h)
		if b := got.Bounds(); b.Dx() != tc.w || b.Dy() != tc.h {
			t.Fatalf("%s: %v, want %dx%d", tc.name, b, tc.w, tc.h)
		}
		if c := got.RGBAAt(tc.at[0].X, tc.at[0].Y); !near(c, red) {
			t.Errorf("%s: at %v %v, want red", tc.name, tc.at[0], c)
		}
		if c := got.RGBAAt(tc.at[1].X, tc.at[1].Y); !near(c, blue) {
			t.Errorf("%s: at %v %v, want blue", tc.name, tc.at[1], c)
		}
	}
	// The middle of a wide one: an even split stays at the middle.
	got := Cover(halves(400, 100, true), 100, 100)
	if !near(got.RGBAAt(45, 50), red) || !near(got.RGBAAt(55, 50), blue) {
		t.Error("the cut isn't around the middle")
	}
}

// Each place on the strip shows the tile nearest its time; the last is
// cut at the end, and a missing tile leaves its place clear.
func TestStrip(t *testing.T) {
	cols := []color.RGBA{red, blue, {0, 0xff, 0, 0xff}, {0xff, 0xff, 0, 0xff}}
	var tiles []*image.RGBA
	for _, c := range cols {
		tiles = append(tiles, solid(80, 50, c))
	}
	s := Strip(tiles, 50, 30, 175)
	if b := s.Bounds(); b.Dx() != 175 || b.Dy() != 30 {
		t.Fatalf("strip %v, want 175x30", b)
	}
	for i, x := range []int{25, 75, 125, 170} {
		if c := s.RGBAAt(x, 15); !near(c, cols[i]) {
			t.Errorf("at x=%d %v, want tile %d", x, c, i)
		}
	}
	tiles[1] = nil
	s = Strip(tiles, 50, 30, 175)
	if c := s.RGBAAt(75, 15); c.A != 0 {
		t.Errorf("a missing tile drew %v", c)
	}
	if c := s.RGBAAt(25, 15); !near(c, red) {
		t.Errorf("the tile before a missing one: %v", c)
	}
}

// frame is a w×h BGRA camera frame, stride bytes a row (the padding
// filled with junk), colored by at(x, y).
func frame(w, h, stride int, at func(x, y int) color.RGBA) []byte {
	pix := make([]byte, (h-1)*stride+w*4)
	for i := range pix {
		pix[i] = 0x5a
	}
	for y := range h {
		for x := range w {
			c := at(x, y)
			o := y*stride + x*4
			pix[o], pix[o+1], pix[o+2], pix[o+3] = c.B, c.G, c.R, c.A
		}
	}
	return pix
}

// A flat color comes out exactly, through each path: copied, halved once
// and twice, enlarged, and with row padding.
func TestBubbleFlat(t *testing.T) {
	c := color.RGBA{0x12, 0x9c, 0xe7, 0xff}
	for _, tc := range []struct{ w, h, stride, d int }{
		{720, 720, 720 * 4, 720},
		{1280, 720, 1280 * 4, 360},
		{1280, 720, 1280*4 + 64, 180},
		{640, 480, 640 * 4, 600},
		{1280, 720, 1280 * 4, 400},
	} {
		var b Bubble
		dst := image.NewRGBA(image.Rect(0, 0, tc.d, tc.d))
		b.Cut(dst, frame(tc.w, tc.h, tc.stride, func(int, int) color.RGBA { return c }), tc.w, tc.h, tc.stride, Crop{})
		for y := range tc.d {
			for x := range tc.d {
				if got := dst.RGBAAt(x, y); got != c {
					t.Fatalf("%dx%d (stride %d) to %d: (%d,%d) is %v, want %v", tc.w, tc.h, tc.stride, tc.d, x, y, got, c)
				}
			}
		}
	}
}

// Only the middle square is kept, whichever way the frame is long, and
// it's mirrored: what's on the frame's left is on the bubble's right.
func TestBubbleMirroredMiddle(t *testing.T) {
	for _, tc := range []struct {
		name string
		w, h int
	}{{"landscape", 1280, 720}, {"portrait", 720, 1280}, {"square", 720, 720}} {
		s := min(tc.w, tc.h)
		ox, oy := (tc.w-s)/2, (tc.h-s)/2
		// Outside the square: green. Inside: red on its left third, blue
		// on the rest.
		pix := frame(tc.w, tc.h, tc.w*4, func(x, y int) color.RGBA {
			switch {
			case x < ox || x >= ox+s || y < oy || y >= oy+s:
				return color.RGBA{0, 0xff, 0, 0xff}
			case x-ox < s/3:
				return red
			}
			return blue
		})
		for _, d := range []int{180, 360, 500} {
			var b Bubble
			dst := image.NewRGBA(image.Rect(0, 0, d, d))
			b.Cut(dst, pix, tc.w, tc.h, tc.w*4, Crop{})
			for _, p := range []struct {
				x, y int
				want color.RGBA
			}{
				{d - 1, d / 2, red}, {d - d/6, 0, red}, {d - d/6, d - 1, red}, // the frame's left, mirrored
				{0, d / 2, blue}, {d / 3, 0, blue}, {d / 2, d - 1, blue},
			} {
				if got := dst.RGBAAt(p.x, p.y); !near(got, p.want) {
					t.Errorf("%s to %d: (%d,%d) is %v, want %v", tc.name, d, p.x, p.y, got, p.want)
				}
			}
		}
	}
}

// A short or empty frame leaves dst alone rather than read past it.
func TestBubbleShortFrame(t *testing.T) {
	var b Bubble
	dst := solid(64, 64, red)
	b.Cut(dst, make([]byte, 100), 1280, 720, 1280*4, Crop{})
	b.Cut(dst, nil, 0, 0, 0, Crop{})
	if dst.RGBAAt(10, 10) != red {
		t.Fatal("a short frame drew")
	}
}

// Zoomed in, the square shrinks about the middle: a red square half the
// middle square's side fills the bubble at zoom 2, and only its middle
// at zoom 1.
func TestBubbleZoom(t *testing.T) {
	const w, h, d = 1280, 720, 300
	pix := frame(w, h, w*4, func(x, y int) color.RGBA {
		if x >= w/2-h/4 && x < w/2+h/4 && y >= h/4 && y < h*3/4 {
			return red
		}
		return blue
	})
	var b Bubble
	dst := image.NewRGBA(image.Rect(0, 0, d, d))
	b.Cut(dst, pix, w, h, w*4, Crop{Zoom: 2})
	for _, p := range [][2]int{{2, 2}, {d - 3, 2}, {d / 2, d / 2}, {2, d - 3}, {d - 3, d - 3}} {
		if got := dst.RGBAAt(p[0], p[1]); !near(got, red) {
			t.Errorf("zoom 2: (%d,%d) is %v, want red", p[0], p[1], got)
		}
	}
	b.Cut(dst, pix, w, h, w*4, Crop{Zoom: 1})
	if got := dst.RGBAAt(d/2, d/2); !near(got, red) {
		t.Errorf("zoom 1: the middle is %v, want red", got)
	}
	if got := dst.RGBAAt(2, 2); !near(got, blue) {
		t.Errorf("zoom 1: the corner is %v, want blue", got)
	}
}

// A Crop's X is in the picture as seen: toward the bubble's right, which
// is the frame's left. Its Y pans a portrait frame.
func TestBubblePan(t *testing.T) {
	const d = 200
	var b Bubble
	dst := image.NewRGBA(image.Rect(0, 0, d, d))
	// The frame's left half red, its right half blue: seen mirrored, red
	// is on the right.
	land := frame(1280, 720, 1280*4, func(x, _ int) color.RGBA {
		if x < 640 {
			return red
		}
		return blue
	})
	for _, tc := range []struct {
		x         float64
		mid, edge color.RGBA // the bubble's middle; its right edge
	}{{0.2, red, red}, {-0.2, blue, red}} {
		b.Cut(dst, land, 1280, 720, 1280*4, Crop{X: tc.x})
		if got := dst.RGBAAt(d/2, d/2); !near(got, tc.mid) {
			t.Errorf("X %v: the middle is %v, want %v", tc.x, got, tc.mid)
		}
		if got := dst.RGBAAt(d-1, d/2); !near(got, tc.edge) {
			t.Errorf("X %v: the right edge is %v, want %v", tc.x, got, tc.edge)
		}
	}
	// A portrait frame's top half red: a positive Y looks lower.
	port := frame(720, 1280, 720*4, func(_, y int) color.RGBA {
		if y < 640 {
			return red
		}
		return blue
	})
	for _, tc := range []struct {
		y   float64
		mid color.RGBA
	}{{0.2, blue}, {-0.2, red}} {
		b.Cut(dst, port, 720, 1280, 720*4, Crop{Y: tc.y})
		if got := dst.RGBAAt(d/2, d/2); !near(got, tc.mid) {
			t.Errorf("portrait Y %v: the middle is %v, want %v", tc.y, got, tc.mid)
		}
	}
}

// A Crop past an edge, or zoomed past either end, is clamped, and the cut
// agrees with the clamped one.
func TestCropClamp(t *testing.T) {
	for _, tc := range []struct {
		c, want Crop
	}{
		{Crop{}, Crop{Zoom: 1}},
		{Crop{Zoom: 10}, Crop{Zoom: MaxZoom}},
		{Crop{Zoom: 1, X: 5, Y: 1}, Crop{Zoom: 1, X: (1 - 720.0/1280) / 2, Y: 0}},
		{Crop{Zoom: 2, X: -1, Y: -1}, Crop{Zoom: 2, X: -(1 - 360.0/1280) / 2, Y: -0.25}},
	} {
		if got := tc.c.Clamp(1280, 720); got != tc.want {
			t.Errorf("%+v: %+v, want %+v", tc.c, got, tc.want)
		}
	}
	pix := frame(1280, 720, 1280*4, func(x, y int) color.RGBA { return color.RGBA{uint8(x), uint8(y), uint8(x ^ y), 0xff} })
	var b Bubble
	far, clamped := image.NewRGBA(image.Rect(0, 0, 150, 150)), image.NewRGBA(image.Rect(0, 0, 150, 150))
	c := Crop{Zoom: 2.5, X: 3, Y: -3}
	b.Cut(far, pix, 1280, 720, 1280*4, c)
	b.Cut(clamped, pix, 1280, 720, 1280*4, c.Clamp(1280, 720))
	if !bytes.Equal(far.Pix, clamped.Pix) {
		t.Error("a crop past the edge cut differently from its clamped self")
	}
	// Panned all the way to the seen right, the square is the frame's
	// left edge.
	if s, x0, y0 := (Crop{Zoom: 2, X: 1, Y: 1}).square(1280, 720); s != 360 || x0 != 0 || y0 != 360 {
		t.Errorf("the far corner: side %d at (%d,%d), want 360 at (0,360)", s, x0, y0)
	}
}

func BenchmarkBubble(b *testing.B) {
	pix := frame(1280, 720, 1280*4, func(x, y int) color.RGBA { return color.RGBA{uint8(x), uint8(y), uint8(x ^ y), 0xff} })
	for _, zoom := range []float64{1, 2} {
		for _, d := range []int{360, 400, 640, 720} {
			b.Run(fmt.Sprintf("zoom%v/%d", zoom, d), func(b *testing.B) {
				var bb Bubble
				dst := image.NewRGBA(image.Rect(0, 0, d, d))
				for b.Loop() {
					bb.Cut(dst, pix, 1280, 720, 1280*4, Crop{Zoom: zoom})
				}
			})
		}
	}
}
