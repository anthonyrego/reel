package main

import (
	"testing"

	"github.com/anthonyrego/reel/internal/draw"
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
