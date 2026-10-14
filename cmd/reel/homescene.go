package main

import (
	"github.com/anthonyrego/reel/internal/draw"
	"github.com/anthonyrego/reel/internal/ui"
	"github.com/anthonyrego/reel/internal/ui/home"
)

// homeScene shows the main window's view in a window.
type homeScene struct {
	v *home.View
	// woken, when set, takes the app's new state when the loop is woken.
	woken func()
	// focused, when set, runs when the window gains focus.
	focused func()
	// drew, when set, hears the recordings drawn each frame.
	drew func(visible []string)
}

func (s *homeScene) draw(c *ui.Ctx, l *draw.List, w, h float32) {
	s.v.Draw(c, l, w, h)
	if s.drew != nil {
		s.drew(s.v.Visible())
	}
	if s.v.Animating(c.Now) {
		c.RequestFrame()
	}
}

func (s *homeScene) event(c *ui.Ctx, ev ui.Event) bool {
	redraw, hand := s.v.Event(c, ev)
	if ev.Kind == ui.PointerMove || ev.Kind == ui.PointerLeave {
		if hand {
			setCursor(ui.CursorPointer)
		} else {
			setCursor(ui.CursorDefault)
		}
	}
	return redraw
}

func (s *homeScene) animating() bool { return false }

func (s *homeScene) focus() {
	if s.focused != nil {
		s.focused()
	}
}

func (s *homeScene) woke() {
	if s.woken != nil {
		s.woken()
	}
}
