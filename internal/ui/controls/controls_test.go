package controls

import (
	"fmt"
	"testing"
	"time"

	"github.com/anthonyrego/reel/internal/draw"
	"github.com/anthonyrego/reel/internal/text"
	"github.com/anthonyrego/reel/internal/theme"
	"github.com/anthonyrego/reel/internal/ui"
	"github.com/anthonyrego/reel/internal/ui/icons"
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

// Hover and clicks keep to the control under the pointer when controls
// before it come and go (a card's buttons), shifting its place in the
// frame.
func TestControlsKeepTheirPlace(t *testing.T) {
	h := NewHits()
	a := draw.Rect{X: 10, Y: 10, W: 50, H: 20}
	extra := draw.Rect{X: 200, Y: 10, W: 50, H: 20}
	clicks := 0
	frame := func(withExtra bool) (overA, overExtra bool) {
		h.Begin()
		if withExtra {
			overExtra = h.add(extra, true, nil)
		}
		return h.add(a, true, func() { clicks++ }), overExtra
	}
	frame(false)
	h.Event(ev(ui.PointerMove, 20, 15))
	if overA, overExtra := frame(true); !overA || overExtra {
		t.Errorf("a control came before the hovered one: over it %v, over the new one %v", overA, overExtra)
	}
	h.Event(ev(ui.PointerDown, 20, 15))
	frame(false) // a redraw between the press and the release
	h.Event(ev(ui.PointerUp, 20, 15))
	if clicks != 1 {
		t.Errorf("pressed and released on it, redrawn between: %d clicks", clicks)
	}
}

// A control drawn under a pointer that hasn't moved takes the hover, a
// frame later.
func TestHoverFollowsTheFrame(t *testing.T) {
	c := ctx(t, time.Now())
	frames := 0
	c.Animate = func() { frames++ }
	h := NewHits()
	card := draw.Rect{W: 200, H: 100}
	btn := draw.Rect{X: 10, Y: 10, W: 30, H: 30}
	var overCard, overBtn bool
	frame := func(withBtn bool) {
		var l draw.List
		h.Begin()
		overCard = h.add(card, true, nil)
		overBtn = false
		if withBtn {
			overBtn = h.add(btn, true, nil)
		}
		h.Tip(c, &l, 400, 300)
	}
	frame(false)
	h.Event(ev(ui.PointerMove, 20, 20))
	frame(true) // the card's buttons show now it's under the pointer
	if frames == 0 {
		t.Fatal("a button came under the pointer: no frame asked to show it")
	}
	frame(true)
	if !overBtn || overCard {
		t.Errorf("the frame after: over the button %v, over the card %v", overBtn, overCard)
	}
}

func ctx(t *testing.T, now time.Time) *ui.Ctx {
	t.Helper()
	s, err := text.NewShaper()
	if err != nil {
		t.Fatal(err)
	}
	return &ui.Ctx{Text: s, Pal: theme.Dark, Scale: 2, Now: now}
}

// An icon button clicks, and its tooltip shows only once the pointer has
// rested on it, until a press.
func TestIconButtonTip(t *testing.T) {
	now := time.Now()
	c := ctx(t, now)
	h := NewHits()
	clicks, frames := 0, 0
	c.Animate = func() { frames++ }
	r := draw.Rect{X: 20, Y: 20, W: 32, H: 32}
	frame := func() {
		var l draw.List
		h.Begin()
		h.IconButton(c, &l, r, icons.Copy, "Copy", IconOpts{}, true, func() { clicks++ })
		h.Tip(c, &l, 400, 300)
	}
	frame()
	h.Event(ev(ui.PointerMove, 36, 36))
	frame()
	if h.TipShown(c.Now) || frames == 0 {
		t.Fatalf("at once: shown %v, frames asked %d", h.TipShown(c.Now), frames)
	}
	c.Now = now.Add(tipDelay)
	frame()
	if !h.TipShown(c.Now) {
		t.Fatal("no tooltip after the delay")
	}
	h.Event(ev(ui.PointerDown, 36, 36))
	h.Event(ev(ui.PointerUp, 36, 36))
	frame()
	if clicks != 1 || h.TipShown(c.Now) {
		t.Fatalf("clicks %d, tooltip after a press %v", clicks, h.TipShown(c.Now))
	}
}

// A menu taller than the window scrolls, opening on the current item, and
// a click picks the row under the pointer.
func TestMenuScrolls(t *testing.T) {
	c := ctx(t, time.Now())
	h := NewHits()
	var choices []Choice
	for i := range 40 {
		choices = append(choices, Choice{ID: fmt.Sprint(i), Label: fmt.Sprint("Window ", i)})
	}
	field := draw.Rect{X: 20, Y: 20, W: 200, H: 32}
	got := ""
	frame := func() {
		var l draw.List
		h.Begin()
		h.Menu(c, &l, field, 600, 400, choices, "30", func(id string) { got = id }, func() { got = "closed" })
	}
	frame()
	if h.menuRect.Bottom() > 400-8 {
		t.Fatalf("the menu %v runs off a 400-tall window", h.menuRect)
	}
	if h.menuScroll == 0 {
		t.Fatal("opened without the current item in view")
	}
	// Scroll to the top; the first row is then under its own place.
	h.Event(ui.Event{Kind: ui.Wheel, Pos: draw.Point{X: 100, Y: 100}, DY: -1e4})
	frame()
	if h.menuScroll != 0 {
		t.Fatalf("scrolled to %v, want 0", h.menuScroll)
	}
	y := h.menuRect.Y + 4 + menuRow/2
	h.Event(ev(ui.PointerMove, 100, y))
	h.Event(ev(ui.PointerDown, 100, y))
	h.Event(ev(ui.PointerUp, 100, y))
	if got != "0" {
		t.Fatalf("picked %q, want 0", got)
	}
}
