package main

import (
	"math"
	"testing"

	"github.com/anthonyrego/reel/internal/draw"
	"github.com/anthonyrego/reel/internal/settings"
	"github.com/anthonyrego/reel/internal/thumb"
)

func TestClampBubble(t *testing.T) {
	screen := draw.Rect{X: 0, Y: 25, W: 1440, H: 875}
	for _, tc := range []struct {
		name     string
		r, bound draw.Rect
		want     draw.Rect
	}{
		{"inside stays", draw.Rect{X: 100, Y: 600, W: 200, H: 200}, screen, draw.Rect{X: 100, Y: 600, W: 200, H: 200}},
		{"off the left and bottom: moved in", draw.Rect{X: -50, Y: 800, W: 200, H: 200}, screen, draw.Rect{X: 0, Y: 700, W: 200, H: 200}},
		{"under the menu bar: moved down", draw.Rect{X: 10, Y: 0, W: 200, H: 200}, screen, draw.Rect{X: 10, Y: 25, W: 200, H: 200}},
		{"too small: grown about its middle", draw.Rect{X: 500, Y: 500, W: 40, H: 40}, screen, draw.Rect{X: 460, Y: 460, W: 120, H: 120}},
		{"too large: shrunk about its middle", draw.Rect{X: 300, Y: 200, W: 600, H: 600}, screen, draw.Rect{X: 390, Y: 290, W: 420, H: 420}},
		{"larger than its bounds: as large as they are", draw.Rect{X: 0, Y: 0, W: 300, H: 300},
			draw.Rect{X: 50, Y: 50, W: 160, H: 100}, draw.Rect{X: 100, Y: 50, W: 100, H: 100}},
	} {
		if got := clampBubble(tc.r, tc.bound, bubbleMin, bubbleMax); got != tc.want {
			t.Errorf("%s: %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func TestFitInto(t *testing.T) {
	area := draw.Rect{X: 200, Y: 100, W: 800, H: 600}
	r := draw.Rect{X: 300, Y: 400, W: 220, H: 220}
	if got := fitInto(r, area); got != r {
		t.Errorf("inside: moved to %+v", got)
	}
	// Outside, or across an edge: the bottom-left corner, as large.
	want := draw.Rect{X: 224, Y: 700 - 24 - 220, W: 220, H: 220}
	for _, out := range []draw.Rect{{X: 1500, Y: 100, W: 220, H: 220}, {X: 150, Y: 400, W: 220, H: 220}} {
		if got := fitInto(out, area); got != want {
			t.Errorf("from %+v: %+v, want %+v", out, got, want)
		}
	}
	// A small area: no bigger than fits.
	small := draw.Rect{X: 0, Y: 0, W: 300, H: 200}
	if got := fitInto(r, small); got.W != 200-48 || got.X != 24 || got.Bottom() != 200-24 {
		t.Errorf("small area: %+v", got)
	}
	// A tiny one: most of it.
	tiny := draw.Rect{X: 0, Y: 0, W: 100, H: 80}
	if got := fitInto(r, tiny); got.W != 64 || got.Right() > 100 || got.Bottom() > 80 || got.X < 0 || got.Y < 0 {
		t.Errorf("tiny area: %+v", got)
	}
}

// A press is on the bubble only inside its shape: a circle's corners
// aren't, a square's are; the handles sit on the outline (a square's
// corners) and take a press near them, while they're shown.
func TestPartAt(t *testing.T) {
	shape := draw.Rect{X: 14, Y: 14, W: 200, H: 200}
	circle, square := float32(100), float32(0)
	for _, tc := range []struct {
		name    string
		p       draw.Point
		rad     float32
		handles bool
		want    bubblePart
	}{
		{"the middle", draw.Point{X: 114, Y: 114}, circle, true, partShape},
		{"a circle's corner", draw.Point{X: 20, Y: 20}, circle, false, partNone},
		{"a square's corner", draw.Point{X: 20, Y: 20}, square, false, partShape},
		{"outside", draw.Point{X: 5, Y: 114}, square, false, partNone},
		{"a circle's size handle", draw.Point{X: 214 - 29.3, Y: 214 - 29.3}, circle, true, partSize},
		{"a circle's round dot", draw.Point{X: 214 - 29.3, Y: 14 + 29.3}, circle, true, partRound},
		{"a square's size handle, just outside", draw.Point{X: 218, Y: 218}, square, true, partSize},
		{"a square's round dot", draw.Point{X: 212, Y: 16}, square, true, partRound},
		{"handles hidden", draw.Point{X: 212, Y: 16}, square, false, partShape},
	} {
		if got := partAt(tc.p, shape, tc.rad, tc.handles); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
	// The handles are on the outline: half a point in along their
	// diagonals is inside, half a point out isn't.
	for _, rad := range []float32{0, 30, 100} {
		rh, sh := handlePoints(shape, rad)
		for _, h := range []struct{ p, in draw.Point }{{rh, draw.Point{X: -0.5, Y: 0.5}}, {sh, draw.Point{X: -0.5, Y: -0.5}}} {
			in := draw.Point{X: h.p.X + h.in.X, Y: h.p.Y + h.in.Y}
			out := draw.Point{X: h.p.X - h.in.X, Y: h.p.Y - h.in.Y}
			if !inShape(in, shape, rad) || inShape(out, shape, rad) {
				t.Errorf("radius %v: a handle at %v, off the outline", rad, h.p)
			}
		}
	}
}

// The round dot follows the pointer along its corner's diagonal: inward
// rounder, the whole way in (about 41 points at 200) from a square to a
// circle.
func TestRoundBy(t *testing.T) {
	const d = 200
	full := float32(d / 2 * (math.Sqrt2 - 1)) // the dot's travel, along the diagonal
	step := full / math.Sqrt2                 // per axis
	for _, tc := range []struct {
		from, dx, dy, want float32
	}{
		{0, -step, step, 1},
		{1, step, -step, 0},
		{0, -step / 2, step / 2, 0.5},
		{0.5, 0, 0, 0.5},
		{0.5, 50, 50, 0.5}, // across the diagonal: no change
		{0, 100, -100, 0},  // past either end: clamped
		{1, -100, 100, 1},
	} {
		if got := roundBy(tc.from, d, tc.dx, tc.dy); math.Abs(float64(got-tc.want)) > 1e-4 {
			t.Errorf("from %v by (%v,%v): %v, want %v", tc.from, tc.dx, tc.dy, got, tc.want)
		}
	}
}

func TestResizeFromCorner(t *testing.T) {
	screen := draw.Rect{X: 0, Y: 25, W: 1440, H: 875}
	from := draw.Rect{X: 100, Y: 500, W: 200, H: 200}
	for _, tc := range []struct {
		name   string
		dx, dy float32
		bounds draw.Rect
		want   float32
	}{
		{"out: larger, the corner stays", 40, 20, screen, 230},
		{"in: smaller", -30, -30, screen, 170},
		{"no smaller than the least", -200, -200, screen, bubbleMin},
		{"no larger than the most", 500, 500, draw.Rect{W: 3000, H: 3000}, bubbleMax},
		{"no further than the bounds' bottom", 300, 300, screen, 400},
		{"a small area: no larger than it", 50, 50, draw.Rect{X: 100, Y: 500, W: 100, H: 100}, 100},
	} {
		got := resizeFromCorner(from, tc.dx, tc.dy, tc.bounds)
		if got.X != from.X || got.Y != from.Y || got.W != tc.want || got.H != tc.want {
			t.Errorf("%s: %+v, want side %v at (100,500)", tc.name, got, tc.want)
		}
	}
}

// Dragging the picture keeps what was under the pointer under it: right
// moves the middle of what's shown left, as far as the frame goes.
func TestPanBy(t *testing.T) {
	const w, h, d = 1280, 720, 200
	// At zoom 1 the square is 720 of 1280 across: 100 points of a 200-point
	// bubble are 360 pixels, 0.28125 of the width.
	got := panBy(thumb.Crop{Zoom: 1}, 50, 0, d, w, h)
	if want := -0.140625; math.Abs(got.X-want) > 1e-6 || got.Y != 0 {
		t.Errorf("right 50: %+v, want X %v", got, want)
	}
	// No room up or down at zoom 1 in a landscape frame; at zoom 2 there is.
	if got := panBy(thumb.Crop{Zoom: 1}, 0, 80, d, w, h); got.Y != 0 {
		t.Errorf("down at zoom 1: %+v", got)
	}
	if got := panBy(thumb.Crop{Zoom: 2}, 0, -40, d, w, h); math.Abs(got.Y-0.1) > 1e-6 {
		t.Errorf("up 40 at zoom 2: %+v, want Y 0.1", got)
	}
	// Far past the edge: at it.
	if got, want := panBy(thumb.Crop{Zoom: 1}, -5000, 0, d, w, h), (thumb.Crop{Zoom: 1, X: 5}).Clamp(w, h); got != want {
		t.Errorf("far left: %+v, want %+v", got, want)
	}
}

func TestLookOf(t *testing.T) {
	if got := lookOf(nil); got != defaultLook {
		t.Errorf("none kept: %+v", got)
	}
	b := &settings.Bubble{Size: 220, Zoom: 1.5, PanX: 0.1, PanY: -0.05, Round: 0.25}
	if got, want := lookOf(b), (bubbleLook{crop: thumb.Crop{Zoom: 1.5, X: 0.1, Y: -0.05}, round: 0.25}); got != want {
		t.Errorf("kept: %+v, want %+v", got, want)
	}
	if got := lookOf(&settings.Bubble{Round: 3}); got.round != 1 {
		t.Errorf("round 3: %v, want 1", got.round)
	}
}
