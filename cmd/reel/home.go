package main

// The home scene (M0): the permissions reel needs, with a way to grant
// each, and what it can record: displays, windows and microphones. It
// gathers state off the main thread and wakes the loop with it.

import (
	"fmt"
	"log/slog"
	"os/exec"
	"sync"

	"github.com/anthonyrego/reel/internal/draw"
	"github.com/anthonyrego/reel/internal/mac"
	"github.com/anthonyrego/reel/internal/text"
	"github.com/anthonyrego/reel/internal/ui"
)

// homeState is what the home scene shows. It is built off the main thread.
type homeState struct {
	screen, mic mac.Permission
	content     mac.Content
	contentErr  error
	mics        []mac.Mic
	micsErr     error
}

// gatherHome reads the permissions and devices. It waits on
// ScreenCaptureKit, so it never runs on the main thread.
func gatherHome() homeState {
	s := homeState{screen: mac.ScreenAccess(), mic: mac.MicAccess()}
	if s.screen == mac.PermissionGranted {
		s.content, s.contentErr = mac.ShareableContent()
	}
	s.mics, s.micsErr = mac.Mics()
	return s
}

// The System Settings panes for each permission.
const (
	screenPane = "x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture"
	micPane    = "x-apple.systempreferences:com.apple.preference.security?Privacy_Microphone"
)

type home struct {
	wake func()

	mu      sync.Mutex
	pending *homeState // published by a refresh, taken by woke
	loading bool

	st      homeState
	have    bool
	tc      textCache
	buttons []button
	hover   int // index into buttons, -1 for none
}

func newHome(wake func()) *home { return &home{wake: wake, hover: -1} }

// refresh gathers the state again on a goroutine. Main thread.
func (h *home) refresh() {
	h.mu.Lock()
	if h.loading {
		h.mu.Unlock()
		return
	}
	h.loading = true
	h.mu.Unlock()
	go func() {
		s := gatherHome()
		h.mu.Lock()
		h.pending, h.loading = &s, false
		h.mu.Unlock()
		h.wake()
	}()
}

func (h *home) woke() {
	h.mu.Lock()
	p := h.pending
	h.pending = nil
	h.mu.Unlock()
	if p != nil {
		h.st, h.have = *p, true
	}
}

func (h *home) animating() bool { return false }

// button is a clickable label drawn this frame.
type button struct {
	r     draw.Rect
	label string
	do    func()
}

func (h *home) event(c *ui.Ctx, ev ui.Event) bool {
	at := -1
	for i, b := range h.buttons {
		if b.r.Contains(ev.Pos) {
			at = i
		}
	}
	switch ev.Kind {
	case ui.PointerMove:
		if at != h.hover {
			h.hover = at
			if at >= 0 {
				setCursor(ui.CursorPointer)
			} else {
				setCursor(ui.CursorDefault)
			}
			return true
		}
	case ui.PointerLeave:
		if h.hover >= 0 {
			h.hover = -1
			return true
		}
	case ui.PointerUp:
		if at >= 0 && ev.Button == 1 {
			h.buttons[at].do()
			return true
		}
	case ui.KeyPress:
		if ev.Key == 'r' && ev.Mods == shortcutMod() {
			h.refresh()
		}
	}
	return false
}

const (
	pad      = 28
	rowH     = 30
	titleSz  = 22
	headSz   = 12
	bodySz   = 13.5
	buttonSz = 12.5
)

func (h *home) draw(c *ui.Ctx, l *draw.List, w, hgt float32) {
	h.buttons = h.buttons[:0]
	pal := c.Pal
	y := float32(pad)
	put := func(s string, font draw.FontID, size, x float32, col draw.Color) float32 {
		lay := h.tc.lay(c, s, font, size)
		drawAt(l, lay, x, y, col)
		return lay.Width
	}

	put("Reel", text.SansSemibold, titleSz, pad, pal.Text)
	y += 34
	put("Records the screen with system audio and your mic in one track.", text.SansRegular, bodySz, pad, pal.TextMuted)
	y += 40

	if !h.have {
		put("Looking…", text.SansRegular, bodySz, pad, pal.TextMuted)
		return
	}

	section := func(name string) {
		put(name, text.SansSemibold, headSz, pad, pal.TextMuted)
		y += 22
		l.Rect(draw.Rect{X: pad, Y: y - 6, W: w - 2*pad, H: 1}, pal.Border, draw.Radii{})
		y += 4
	}

	section("PERMISSIONS")
	h.permission(c, l, &y, w, "Screen & System Audio Recording", h.st.screen, func() {
		if !mac.RequestScreenAccess() {
			openURL(screenPane)
		}
		h.refresh()
	})
	h.permission(c, l, &y, w, "Microphone", h.st.mic, func() {
		if h.st.mic == mac.PermissionDenied {
			openURL(micPane)
			return
		}
		if err := mac.RequestMicAccess(func(bool) { h.refresh() }); err != nil {
			slog.Error("home: mic access", "err", err)
		}
	})
	y += 18

	section("DISPLAYS")
	switch {
	case h.st.screen != mac.PermissionGranted:
		put("Needs Screen Recording.", text.SansRegular, bodySz, pad, pal.TextFaint)
		y += rowH
	case h.st.contentErr != nil:
		put(h.st.contentErr.Error(), text.SansRegular, bodySz, pad, pal.Error)
		y += rowH
	default:
		for _, d := range h.st.content.Displays {
			put(fmt.Sprintf("Display %d", d.ID), text.SansRegular, bodySz, pad, pal.Text)
			put(fmt.Sprintf("%.0f × %.0f points  ·  %.0fx", d.Frame.W, d.Frame.H, d.Scale), text.SansRegular, bodySz, pad+160, pal.TextMuted)
			y += rowH
		}
		put(fmt.Sprintf("%d windows of other apps", len(h.st.content.Windows)), text.SansRegular, bodySz, pad, pal.TextMuted)
		y += rowH
	}
	y += 18

	section("MICROPHONES")
	if h.st.micsErr != nil {
		put(h.st.micsErr.Error(), text.SansRegular, bodySz, pad, pal.Error)
		return
	}
	if len(h.st.mics) == 0 {
		put("None connected.", text.SansRegular, bodySz, pad, pal.TextFaint)
	}
	for _, m := range h.st.mics {
		x := pad + put(m.Name, text.SansRegular, bodySz, pad, pal.Text)
		note := m.Transport.String()
		if m.Default {
			note += "  ·  default"
		}
		put(note, text.SansRegular, bodySz, x+12, pal.TextMuted)
		y += rowH
	}
}

// permission draws one permission row: its name, its state and, unless
// it's granted, a button that asks for it.
func (h *home) permission(c *ui.Ctx, l *draw.List, y *float32, w float32, name string, p mac.Permission, ask func()) {
	pal := c.Pal
	drawAt(l, h.tc.lay(c, name, text.SansRegular, bodySz), pad, *y, pal.Text)
	state, col := p.String(), pal.TextMuted
	switch p {
	case mac.PermissionGranted:
		col = pal.Success
	case mac.PermissionDenied:
		col = pal.Warning
	}
	right := w - pad
	if p != mac.PermissionGranted {
		label := "Allow…"
		if p == mac.PermissionDenied {
			label = "Open Settings…"
		}
		lay := h.tc.lay(c, label, text.SansSemibold, buttonSz)
		r := draw.Rect{X: right - lay.Width - 24, Y: *y - 5, W: lay.Width + 24, H: 26}
		fill := pal.SurfaceAlt
		if len(h.buttons) == h.hover {
			fill = pal.Border
		}
		l.BorderedRect(r, fill, pal.Border, 1, draw.Uniform(6))
		drawAt(l, lay, r.X+12, r.Y+5, pal.Text)
		h.buttons = append(h.buttons, button{r: r, label: label, do: ask})
		right = r.X - 14
	}
	sl := h.tc.lay(c, state, text.SansRegular, bodySz)
	drawAt(l, sl, right-sl.Width, *y, col)
	*y += rowH + 4
}

// openURL opens a URL with the system's handler.
func openURL(u string) {
	if err := exec.Command("open", u).Start(); err != nil {
		slog.Error("open", "url", u, "err", err)
	}
}
