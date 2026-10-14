// Package trim is the trim screen: one recording's picture, a track under
// it with handles for where the copy starts and ends, and saving the copy
// beside the recording or in its place. Dragging a handle shows the
// picture there. Pure: it draws a State and calls Actions; the app owns
// both, and the view owns the range being chosen.
package trim

import (
	"fmt"
	"time"

	"github.com/anthonyrego/reel/internal/draw"
	"github.com/anthonyrego/reel/internal/text"
	"github.com/anthonyrego/reel/internal/ui"
	"github.com/anthonyrego/reel/internal/ui/controls"
)

// State is what the screen shows that the app knows.
type State struct {
	Name     string // the recording's
	Duration time.Duration
	// Width and Height are the picture's size, for its shape before the
	// first one comes.
	Width, Height float64
	// Frame is the picture shown (0: none yet), the one at Shown.
	Frame draw.ImageID
	Shown time.Duration
	// Busy says what's happening while the copy is written ("Saving…"):
	// nothing can be changed.
	Busy string
	// Error is the last thing that went wrong.
	Error string
}

// Actions are what the screen does. Any may be nil (drawn disabled).
type Actions struct {
	// Show asks for the picture at t (the app sets Frame and Shown when
	// it comes; asking again before then replaces the request).
	Show func(t time.Duration)
	// Copy saves in..out beside the recording; Replace puts it in the
	// recording's place.
	Copy, Replace func(in, out time.Duration)
	Cancel        func()
}

// View draws a State and routes input to Actions.
type View struct {
	St  State
	Act Actions

	hits    *controls.Hits
	in, out time.Duration
	moved   handle // the handle the arrow keys nudge
	drag    handle // the handle being dragged, or none
	confirm bool   // Replace asks again
	track   draw.Rect
}

type handle int

const (
	none handle = iota
	start
	end
)

// New returns a view of st keeping all of it.
func New(st State) *View {
	return &View{St: st, hits: controls.NewHits(), out: st.Duration, moved: start}
}

// Range is the part kept.
func (v *View) Range() (in, out time.Duration) { return v.in, v.out }

// SetRange sets the part kept, for renders.
func (v *View) SetRange(in, out time.Duration) { v.in, v.out = in, out }

// AskReplace shows Replace's second ask, for renders.
func (v *View) AskReplace() { v.confirm = true }

const (
	pad       = 28
	titleSize = 22
	bodySize  = 13.5
	smallSize = 12
	trackH    = 40
	handleW   = 12
	// minKeep is the shortest copy.
	minKeep = 500 * time.Millisecond
	// nudge is how far an arrow key moves a handle (shift: bigNudge).
	nudge    = 100 * time.Millisecond
	bigNudge = time.Second
)

// Draw lays out and draws the screen at w×h.
func (v *View) Draw(c *ui.Ctx, l *draw.List, w, h float32) {
	v.hits.Begin()
	pal := c.Pal
	t := v.hits.Texts()
	st := &v.St
	busy := st.Busy != ""
	y := float32(24)

	// Title, the recording, Cancel.
	title := t.Lay(c, "Trim", text.SansSemibold, titleSize)
	controls.Put(l, title, pad, y, pal.Text)
	bw := v.hits.ButtonWidth(c, "Cancel", controls.Secondary)
	v.hits.Button(c, l, w-pad-bw, y-1, "Cancel", controls.Secondary, !busy && v.Act.Cancel != nil, v.Act.Cancel)
	nx := pad + title.Width + 14
	t.PutFit(c, l, st.Name, text.SansRegular, bodySize, nx, y+7, w-pad-bw-16-nx, pal.TextMuted)
	y += 54

	// The bottom, from the bottom up: buttons, times, track.
	by := h - pad - 28
	ty := by - 34
	v.track = draw.Rect{X: pad + handleW, Y: ty - 12 - trackH, W: w - 2*pad - 2*handleW, H: trackH}

	// The picture, as large as fits above the track.
	box := draw.Rect{X: pad, Y: y, W: w - 2*pad, H: v.track.Y - 20 - y}
	pic := fit(box, st.Width, st.Height)
	l.Rect(box, pal.SurfaceAlt, draw.Uniform(8))
	if st.Frame != 0 {
		l.Image(pic, st.Frame, draw.Radii{}, 255)
	} else {
		lay := t.Lay(c, "Loading…", text.SansRegular, bodySize)
		controls.Put(l, lay, box.X+(box.W-lay.Width)/2, box.Y+(box.H-lay.Height)/2, pal.TextMuted)
	}

	v.drawTrack(c, l)

	// Times: the start under the left, the end under the right, what's
	// kept between.
	tl := t.Lay(c, "Start "+Stamp(v.in), text.SansRegular, smallSize)
	controls.Put(l, tl, pad, ty, pal.TextMuted)
	tr := t.Lay(c, "End "+Stamp(v.out), text.SansRegular, smallSize)
	controls.Put(l, tr, w-pad-tr.Width, ty, pal.TextMuted)
	kept := fmt.Sprintf("Keeps %s of %s", Length(v.out-v.in), Length(st.Duration))
	tk := t.Lay(c, kept, text.SansSemibold, smallSize)
	controls.Put(l, tk, (w-tk.Width)/2, ty, pal.Text)

	// Buttons, right to left, and a line on the left: what's happening,
	// what went wrong, or what Replace does.
	right := w - pad
	note, noteCol := "", pal.TextMuted
	cut := v.in > 0 || v.out < st.Duration
	ok := cut && !busy
	if v.confirm {
		for _, b := range []struct {
			label string
			style controls.Style
			do    func()
		}{
			{"Replace", controls.Danger, func() { v.confirm = false; call2(v.Act.Replace, v.in, v.out) }},
			{"Keep", controls.Secondary, func() { v.confirm = false }},
		} {
			bw := v.hits.ButtonWidth(c, b.label, b.style)
			v.hits.Button(c, l, right-bw, by, b.label, b.style, ok && v.Act.Replace != nil, b.do)
			right -= bw + 8
		}
		note = "Replace the recording with the trim? The original goes to the Trash."
	} else {
		bw := v.hits.ButtonWidth(c, "Save as copy", controls.Primary)
		v.hits.Button(c, l, right-bw, by, "Save as copy", controls.Primary, ok && v.Act.Copy != nil, func() { call2(v.Act.Copy, v.in, v.out) })
		right -= bw + 8
		bw = v.hits.ButtonWidth(c, "Replace…", controls.Secondary)
		v.hits.Button(c, l, right-bw, by, "Replace…", controls.Secondary, ok && v.Act.Replace != nil, func() { v.confirm = true })
		right -= bw + 8
		if !cut {
			note = "Drag the ends of the track to choose what to keep."
		}
	}
	switch {
	case busy:
		note, noteCol = st.Busy, pal.TextMuted
	case st.Error != "":
		note, noteCol = st.Error, pal.Error
	}
	if note != "" {
		t.PutFit(c, l, note, text.SansRegular, smallSize, pad, by+7, right-pad-8, noteCol)
	}
}

// drawTrack draws the track: the whole recording, the part kept framed in
// the accent with a handle at each end, and a line where the picture is.
func (v *View) drawTrack(c *ui.Ctx, l *draw.List) {
	pal := c.Pal
	r := v.track
	l.BorderedRect(r, pal.SurfaceAlt, pal.Border, 1, draw.Uniform(6))
	x0, x1 := v.x(v.in), v.x(v.out)
	kept := draw.Rect{X: x0, Y: r.Y, W: x1 - x0, H: r.H}
	l.Rect(kept, pal.Accent.WithAlpha(0x30), draw.Radii{})
	l.Rect(draw.Rect{X: x0, Y: r.Y, W: x1 - x0, H: 3}, pal.Accent, draw.Radii{})
	l.Rect(draw.Rect{X: x0, Y: r.Bottom() - 3, W: x1 - x0, H: 3}, pal.Accent, draw.Radii{})
	if v.St.Frame != 0 && v.St.Duration > 0 {
		sx := v.x(v.St.Shown)
		l.Rect(draw.Rect{X: sx - 1, Y: r.Y - 4, W: 2, H: r.H + 8}, pal.Text, draw.Uniform(1))
	}
	for _, hd := range []handle{start, end} {
		col := pal.Accent
		if v.drag == hd {
			col = col.WithAlpha(0xd8)
		}
		hr := v.handleRect(hd)
		l.Rect(hr, col, draw.Uniform(4))
		l.Rect(draw.Rect{X: hr.X + hr.W/2 - 1, Y: hr.Y + hr.H/2 - 8, W: 2, H: 16}, pal.Surface.WithAlpha(0xc0), draw.Uniform(1))
	}
}

// handleRect is where a handle is drawn: outside the part kept, against
// its edge.
func (v *View) handleRect(hd handle) draw.Rect {
	r := v.track
	if hd == start {
		return draw.Rect{X: v.x(v.in) - handleW, Y: r.Y - 2, W: handleW, H: r.H + 4}
	}
	return draw.Rect{X: v.x(v.out), Y: r.Y - 2, W: handleW, H: r.H + 4}
}

// x is where time d is on the track.
func (v *View) x(d time.Duration) float32 {
	if v.St.Duration <= 0 {
		return v.track.X
	}
	return v.track.X + v.track.W*float32(float64(d)/float64(v.St.Duration))
}

// at is the time at x on the track.
func (v *View) at(x float32) time.Duration {
	if v.track.W <= 0 {
		return 0
	}
	f := float64((x - v.track.X) / v.track.W)
	return time.Duration(f * float64(v.St.Duration))
}

// set moves a handle to d, keeping the part kept at least minKeep long
// and inside the recording, and shows the picture there: at the start,
// the first frame kept; at the end, the last.
func (v *View) set(hd handle, d time.Duration) {
	keep := min(minKeep, v.St.Duration)
	switch hd {
	case start:
		v.in = max(0, min(d, v.out-keep))
		d = v.in
	case end:
		v.out = min(v.St.Duration, max(d, v.in+keep))
		d = max(0, v.out-time.Millisecond)
	default:
		return
	}
	v.moved = hd
	if v.Act.Show != nil {
		v.Act.Show(d)
	}
}

// Event handles input and reports whether to redraw and whether the
// pointer is over something clickable.
func (v *View) Event(c *ui.Ctx, ev ui.Event) (redraw, hand bool) {
	busy := v.St.Busy != ""
	switch ev.Kind {
	case ui.KeyPress:
		switch {
		case ev.Key == ui.KeyEscape && v.confirm:
			v.confirm = false
			return true, false
		case ev.Key == ui.KeyEscape && !busy && v.Act.Cancel != nil:
			v.Act.Cancel()
			return true, false
		case (ev.Key == ui.KeyArrowLeft || ev.Key == ui.KeyArrowRight) && !busy:
			step := nudge
			if ev.Mods&ui.Shift != 0 {
				step = bigNudge
			}
			if ev.Key == ui.KeyArrowLeft {
				step = -step
			}
			d := v.in
			if v.moved == end {
				d = v.out
			}
			v.set(v.moved, d+step)
			return true, false
		case ev.Key == ui.KeyEnter && !busy && !v.confirm && (v.in > 0 || v.out < v.St.Duration):
			call2(v.Act.Copy, v.in, v.out)
			return true, false
		}
	case ui.PointerDown:
		if ev.Button == 1 && !busy && v.grab(ev.Pos) {
			return true, true
		}
	case ui.PointerMove:
		if v.drag != none {
			v.set(v.drag, v.at(ev.Pos.X))
			return true, true
		}
	case ui.PointerUp:
		if v.drag != none {
			v.drag = none
			return true, v.onTrack(ev.Pos)
		}
	}
	r, _, hand := v.hits.Event(ev)
	return r, hand || (!busy && v.onTrack(ev.Pos))
}

// grab starts dragging what's at p: a handle, or, anywhere on the track,
// the nearer handle, moved there.
func (v *View) grab(p draw.Point) bool {
	for _, hd := range []handle{start, end} {
		if grow(v.handleRect(hd), 6).Contains(p) {
			v.drag = hd
			return true
		}
	}
	if !v.onTrack(p) {
		return false
	}
	d := v.at(p.X)
	hd := start
	if d-v.in > v.out-d {
		hd = end
	}
	v.drag = hd
	v.set(hd, d)
	return true
}

func (v *View) onTrack(p draw.Point) bool { return grow(v.track, 6).Contains(p) }

// Animating reports whether a frame is still moving (nothing does).
func (v *View) Animating(time.Time) bool { return false }

// fit is the largest rect of the picture's shape (16:9 when unknown)
// centered in box.
func fit(box draw.Rect, w, h float64) draw.Rect {
	if w <= 0 || h <= 0 {
		w, h = 16, 9
	}
	aspect := float32(w / h)
	pw, ph := box.W, box.W/aspect
	if ph > box.H {
		pw, ph = box.H*aspect, box.H
	}
	return draw.Rect{X: box.X + (box.W-pw)/2, Y: box.Y + (box.H-ph)/2, W: pw, H: ph}
}

func grow(r draw.Rect, by float32) draw.Rect {
	return draw.Rect{X: r.X - by, Y: r.Y - by, W: r.W + 2*by, H: r.H + 2*by}
}

func call2[A, B any](fn func(A, B), a A, b B) {
	if fn != nil {
		fn(a, b)
	}
}

// Stamp writes a time on the track as m:ss.t (h:mm:ss.t past an hour).
func Stamp(d time.Duration) string {
	ds := int((d + 50*time.Millisecond) / (100 * time.Millisecond)) // tenths
	s, tenth := ds/10, ds%10
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d.%d", s/3600, s/60%60, s%60, tenth)
	}
	return fmt.Sprintf("%d:%02d.%d", s/60, s%60, tenth)
}

// Length writes a length: "3.0 s" under a minute, then m:ss.
func Length(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.1f s", d.Seconds())
	}
	s := int(d.Round(time.Second) / time.Second)
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}
