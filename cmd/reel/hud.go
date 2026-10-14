package main

// The HUD: while a recording runs, the main window steps aside (so a click
// on reel never brings it over what's being recorded) and a small pill
// floats at the top of the display: the clock, Pause or Resume, and Stop.
// It's one of reel's windows, so no recording ever shows it; drag it by
// its clock.

import (
	"github.com/Zyko0/go-sdl3/sdl"
	"log/slog"

	"github.com/anthonyrego/reel/internal/draw"
	"github.com/anthonyrego/reel/internal/gfx"
	"github.com/anthonyrego/reel/internal/platform"
	"github.com/anthonyrego/reel/internal/text"
	"github.com/anthonyrego/reel/internal/ui"
	"github.com/anthonyrego/reel/internal/ui/controls"
	"github.com/anthonyrego/reel/internal/ui/home"
)

const (
	hudW, hudH = 196, 44
	hudTop     = 10 // below the menu bar
)

type hud struct {
	a    *winApp
	c    *controller
	s    *surface
	hits *controls.Hits

	// drag is where a drag by the clock began: the pointer and the window,
	// in global points.
	dragging     bool
	fromX, fromY float32
	winX, winY   int32
}

func newHUD(a *winApp, c *controller) *hud { return &hud{a: a, c: c, hits: controls.NewHits()} }

// live shows the HUD while a recording runs, and the main window again
// when it ends.
func (h *hud) live(on bool) {
	main := h.a.window.Handle()
	if !on {
		if h.s != nil {
			h.a.removeWindow(h.s)
			h.s = nil
		}
		main.Show()
		main.Raise()
		return
	}
	if h.s == nil {
		s, err := h.a.addWindow(gfx.WindowConfig{Title: "Reel", Width: hudW, Height: hudH, Hidden: true, Overlay: true}, h)
		if err != nil {
			slog.Error("hud: open", "err", err)
			return
		}
		s.transparent = true
		h.s = s
	}
	w := h.s.win.Handle()
	// The top middle of the display the main window is on.
	if u, err := sdl.GetDisplayForWindow(main).UsableBounds(); err == nil {
		w.SetPosition(u.X+(u.W-hudW)/2, u.Y+hudTop)
	}
	if err := platform.Float(w, platform.Floating{SeeThrough: true, Level: platform.LevelFloating}); err != nil {
		slog.Warn("hud: float", "err", err)
	}
	main.Hide()
	showWindow(h.s.win, false)
}

// showWindow shows w, taking focus only if activate.
//
// Copied from an earlier project of the author's.
func showWindow(w *gfx.Window, activate bool) {
	if !activate {
		if err := sdl.SetHint(sdl.HINT_WINDOW_ACTIVATE_WHEN_SHOWN, "0"); err == nil {
			defer sdl.SetHint(sdl.HINT_WINDOW_ACTIVATE_WHEN_SHOWN, "1")
		}
	}
	if err := w.Handle().Show(); err != nil {
		slog.Warn("app: show window", "err", err)
	}
}

func (h *hud) draw(c *ui.Ctx, l *draw.List, w, ht float32) {
	h.hits.Begin()
	t := h.hits.Texts()
	pal := c.Pal
	bg, fg, muted, red, amber := pal.Bar, pal.Text, pal.TextMuted, pal.Live, pal.Warning
	l.Rect(draw.Rect{W: w, H: ht}, bg, draw.Uniform(ht/2))

	st := h.c.v.St
	live := st.Live
	if live == nil {
		live = &home.Live{}
	}
	dot, label := red, home.Clock(live.Elapsed)
	switch {
	case st.Finishing:
		dot, label = muted, "Saving…"
	case live.Hidden:
		dot, label = amber, label+"  hidden"
	case live.Paused:
		dot = muted
	}
	mid := ht / 2
	l.Rect(draw.Rect{X: 18, Y: mid - 5, W: 10, H: 10}, dot, draw.Uniform(5))
	lay := t.Lay(c, label, text.SansSemibold, 14)
	controls.Put(l, lay, 36, mid-lay.Height/2, fg)

	// Stop, then Pause or Resume, from the right: the main window's own
	// actions, which show what they did there too.
	const btn = 32
	stop := draw.Rect{X: w - 8 - btn, Y: mid - btn/2, W: btn, H: btn}
	pause := draw.Rect{X: stop.X - 4 - btn, Y: stop.Y, W: btn, H: btn}
	enabled := !st.Finishing
	if h.hits.Area(stop, func() {
		if enabled {
			h.c.v.Act.Stop()
		}
	}) && enabled {
		l.Rect(stop, fg.WithAlpha(0x22), draw.Uniform(btn/2))
	}
	l.Rect(draw.Rect{X: stop.X + 10, Y: stop.Y + 10, W: 12, H: 12}, red, draw.Uniform(2.5))

	paused := live.Paused && !live.Hidden
	if h.hits.Area(pause, func() {
		if !enabled {
			return
		}
		if paused {
			h.c.v.Act.Resume()
		} else {
			h.c.v.Act.Pause()
		}
	}) && enabled {
		l.Rect(pause, fg.WithAlpha(0x22), draw.Uniform(btn/2))
	}
	cx, cy := pause.X+btn/2, pause.Y+btn/2
	if paused {
		// Resume: a triangle.
		l.Path(draw.Path{Points: []draw.Point{{X: cx - 4, Y: cy - 6}, {X: cx + 6, Y: cy}, {X: cx - 4, Y: cy + 6}}, Closed: true, Fill: fg})
	} else {
		l.Rect(draw.Rect{X: cx - 5, Y: cy - 6, W: 3.5, H: 12}, fg, draw.Uniform(1))
		l.Rect(draw.Rect{X: cx + 1.5, Y: cy - 6, W: 3.5, H: 12}, fg, draw.Uniform(1))
	}
}

func (h *hud) event(c *ui.Ctx, ev ui.Event) bool {
	redraw, onControl, hand := h.hits.Event(ev)
	switch ev.Kind {
	case ui.PointerDown:
		if !onControl && ev.Button == 1 && h.s != nil {
			_, gx, gy := sdl.GetGlobalMouseState()
			x, y, err := h.s.win.Handle().Position()
			if err == nil {
				h.dragging, h.fromX, h.fromY, h.winX, h.winY = true, gx, gy, x, y
			}
		}
	case ui.PointerMove:
		if h.dragging && h.s != nil {
			_, gx, gy := sdl.GetGlobalMouseState()
			h.s.win.Handle().SetPosition(h.winX+int32(gx-h.fromX), h.winY+int32(gy-h.fromY))
		}
	case ui.PointerUp, ui.PointerLeave:
		if ev.Kind == ui.PointerUp {
			h.dragging = false
		}
	}
	if ev.Kind == ui.PointerMove || ev.Kind == ui.PointerLeave {
		if hand {
			setCursor(ui.CursorPointer)
		} else {
			setCursor(ui.CursorDefault)
		}
	}
	return redraw
}

func (h *hud) animating() bool { return false }
func (h *hud) woke()           {}
