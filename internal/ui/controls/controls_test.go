package controls

import (
	"testing"

	"github.com/anthonyrego/reel/internal/draw"
	"github.com/anthonyrego/reel/internal/ui"
)

func ev(k ui.EventKind, x, y float32) ui.Event {
	return ui.Event{Kind: k, Pos: draw.Point{X: x, Y: y}, Button: 1}
}

func TestClick(t *testing.T) {
	h := NewHits()
	clicks := 0
	h.Begin()
	h.add(draw.Rect{X: 10, Y: 10, W: 50, H: 20}, true, func() { clicks++ })
	h.add(draw.Rect{X: 100, Y: 10, W: 50, H: 20}, false, func() { clicks += 100 })

	h.Event(ev(ui.PointerDown, 20, 15))
	h.Event(ev(ui.PointerUp, 25, 18))
	if clicks != 1 {
		t.Fatalf("a click ran %d times", clicks)
	}
	// Pressed on one control, released off it: nothing.
	h.Event(ev(ui.PointerDown, 20, 15))
	h.Event(ev(ui.PointerUp, 200, 15))
	// A disabled control never runs.
	h.Event(ev(ui.PointerDown, 110, 15))
	h.Event(ev(ui.PointerUp, 110, 15))
	if clicks != 1 {
		t.Fatalf("clicks = %d", clicks)
	}
	if _, _, hand := h.Event(ev(ui.PointerMove, 110, 15)); hand {
		t.Error("the hand cursor over a disabled control")
	}
	if redraw, _, hand := h.Event(ev(ui.PointerMove, 20, 15)); !redraw || !hand {
		t.Errorf("hovering: redraw %v hand %v", redraw, hand)
	}
}

// The last control drawn is on top (a menu over the window).
func TestTopmostWins(t *testing.T) {
	h := NewHits()
	got := ""
	h.Begin()
	h.add(draw.Rect{W: 100, H: 100}, true, func() { got = "under" })
	h.add(draw.Rect{X: 10, Y: 10, W: 20, H: 20}, true, func() { got = "over" })
	h.Event(ev(ui.PointerDown, 15, 15))
	h.Event(ev(ui.PointerUp, 15, 15))
	if got != "over" {
		t.Errorf("got %q", got)
	}
}
