package thumb

import (
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
