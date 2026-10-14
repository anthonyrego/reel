// Package trim is the trim screen: one recording's picture, a filmstrip
// under it with handles for where the copy starts and ends, and saving
// the copy beside the recording or in its place. Dragging a handle shows the
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
	"github.com/anthonyrego/reel/internal/ui/icons"
)

// State is what the screen shows that the app knows.
type State struct {
	Name string // the recording's
	// Title is what it's called on the screen (when it was made); ""
	// shows Name.
	Title    string
	Duration time.Duration
	// Width and Height are the picture's size, for its shape before the
	// first one comes.
	Width, Height float64
	// Frame is the picture shown (0: none yet), the one at Shown.
	Frame draw.ImageID
	Shown time.Duration
	// Strip is the filmstrip drawn in the track (0: none yet), made for
	// the size TrackPixels says.
	Strip draw.ImageID
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

// TrackPixels is the track's size in pixels at scale, as last drawn: the
// filmstrip to make (0, 0 before the first draw).
func (v *View) TrackPixels(scale float32) (w, h int) {
	return int(v.track.W*scale + 0.5), int(v.track.H*scale + 0.5)
}

const (
	pad       = 20
	bodySize  = 13
	smallSize = 12
	handleW   = 14
	frameW    = 3 // the kept part's frame, top and bottom
	// minKeep is the shortest copy.
	minKeep = 500 * time.Millisecond
	// nudge is how far an arrow key moves a handle (shift: bigNudge).
	nudge    = 100 * time.Millisecond
	bigNudge = time.Second
)

// TrackH is the track's height (the filmstrip's).
const TrackH = 52

const trackH = TrackH

// Draw lays out and draws the screen at w×h.
func (v *View) Draw(c *ui.Ctx, l *draw.List, w, h float32) {
	v.hits.Begin()
	pal := c.Pal
	t := v.hits.Texts()
	st := &v.St
	busy := st.Busy != ""

	// Back to the recordings, and what this is.
	v.back(c, l, 10, !busy && v.Act.Cancel != nil)
	title := st.Title
	if title == "" {
		title = st.Name
	}
	tl := t.Lay(c, title, text.SansSemibold, 14)
	dl := t.Lay(c, Length(st.Duration), text.SansRegular, bodySize)
	tw := min(tl.Width, w-2*150)
	tx := (w - tw - 10 - dl.Width) / 2
	t.PutFit(c, l, title, text.SansSemibold, 14, tx, 26-tl.Height/2, tw, pal.Text)
	controls.Put(l, dl, tx+tw+10, 26-dl.Height/2+0.5, pal.TextMuted)
	y := float32(52)

	// The bottom, from the bottom up: buttons, times, track.
	by := h - pad - 28
	ty := by - 30
	v.track = draw.Rect{X: pad + handleW, Y: ty - 8 - trackH, W: w - 2*pad - 2*handleW, H: trackH}

	// The picture, as large as fits above the track.
	box := draw.Rect{X: pad, Y: y, W: w - 2*pad, H: v.track.Y - 18 - y}
	pic := fit(box, st.Width, st.Height)
	l.Rect(box, pal.Surface, draw.Uniform(12))
	if st.Frame != 0 {
		l.Image(pic, st.Frame, draw.Uniform(min(12, pic.W/2)), 255)
	} else {
		lay := t.Lay(c, "Loading…", text.SansRegular, bodySize)
		controls.Put(l, lay, box.X+(box.W-lay.Width)/2, box.Y+(box.H-lay.Height)/2, pal.TextMuted)
	}

	v.drawTrack(c, l)
	v.times(c, l, w, ty)

	// Buttons, right to left, and a line on the left: what's kept, or
	// what's happening, what went wrong, what Replace does.
	right := w - pad
	note, noteCol, noteFont := fmt.Sprintf("Keeps %s of %s", Length(v.out-v.in), Length(st.Duration)), pal.Text, text.SansSemibold
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
		note, noteCol, noteFont = "Replace the recording with the trim? The original goes to the Trash.", pal.TextMuted, text.SansRegular
	} else {
		bw := v.hits.ButtonWidth(c, "Save as copy", controls.Primary)
		v.hits.Button(c, l, right-bw, by, "Save as copy", controls.Primary, ok && v.Act.Copy != nil, func() { call2(v.Act.Copy, v.in, v.out) })
		right -= bw + 8
		bw = v.hits.ButtonWidth(c, "Replace…", controls.Secondary)
		v.hits.Button(c, l, right-bw, by, "Replace…", controls.Secondary, ok && v.Act.Replace != nil, func() { v.confirm = true })
		right -= bw + 8
		if !cut {
			note, noteCol, noteFont = "Drag the ends of the track to choose what to keep.", pal.TextMuted, text.SansRegular
		}
	}
	switch {
	case busy:
		note, noteCol, noteFont = st.Busy, pal.TextMuted, text.SansRegular
	case st.Error != "":
		note, noteCol, noteFont = st.Error, pal.Error, text.SansRegular
	}
	nl := t.Lay(c, note, noteFont, bodySize)
	t.PutFit(c, l, note, noteFont, bodySize, pad, by+(28-nl.Height)/2, right-pad-8, noteCol)
	v.hits.Tip(c, l, w, h)
}

// back draws "‹ Recordings" at the top left: Cancel.
func (v *View) back(c *ui.Ctx, l *draw.List, y float32, enabled bool) {
	pal := c.Pal
	lay := v.hits.Texts().Lay(c, "Recordings", text.SansRegular, bodySize)
	r := draw.Rect{X: pad - 8, Y: y, W: lay.Width + 38, H: 32}
	col := pal.TextMuted
	switch {
	case v.hits.AreaTip(r, "Back to the recordings (Esc)", enabled, v.Act.Cancel):
		col = pal.Text
		l.Rect(r, pal.Text.WithAlpha(0x16), draw.Uniform(r.H/2))
	case !enabled:
		col = pal.TextFaint
	}
	icons.ChevronLeft(l, r.X+16, r.Y+r.H/2, 16, col)
	controls.Put(l, lay, r.X+28, r.Y+(r.H-lay.Height)/2, col)
}

// drawTrack draws the track: the filmstrip, darkened outside the part
// kept, which is framed in the trim color with a handle at each end, and
// a line where the picture is.
func (v *View) drawTrack(c *ui.Ctx, l *draw.List) {
	pal := c.Pal
	r := v.track
	if v.St.Strip != 0 {
		l.Rect(r, pal.Surface, draw.Uniform(6))
		l.Image(r, v.St.Strip, draw.Uniform(6), 255)
	} else {
		l.BorderedRect(r, pal.Surface, pal.Border, 1, draw.Uniform(6))
	}
	x0, x1 := v.x(v.in), v.x(v.out)
	shade := draw.Color{A: 0x99}
	if x0 > r.X {
		l.Rect(draw.Rect{X: r.X, Y: r.Y, W: x0 - r.X, H: r.H}, shade, draw.Radii{6, 0, 0, 6})
	}
	if x1 < r.Right() {
		l.Rect(draw.Rect{X: x1, Y: r.Y, W: r.Right() - x1, H: r.H}, shade, draw.Radii{0, 6, 6, 0})
	}
	col := pal.Trim
	if v.St.Busy != "" {
		col = pal.TextMuted
	}
	l.Rect(draw.Rect{X: x0, Y: r.Y - 2, W: x1 - x0, H: frameW}, col, draw.Radii{})
	l.Rect(draw.Rect{X: x0, Y: r.Bottom() - frameW + 2, W: x1 - x0, H: frameW}, col, draw.Radii{})
	if v.St.Frame != 0 && v.St.Duration > 0 {
		sx := v.x(v.St.Shown)
		l.Rect(draw.Rect{X: sx - 1, Y: r.Y - 6, W: 2, H: r.H + 12}, pal.Text, draw.Uniform(1))
	}
	for _, hd := range []handle{start, end} {
		hc := col
		if v.drag == hd {
			hc = hc.WithAlpha(0xd8)
		}
		hr := v.handleRect(hd)
		radii := draw.Radii{6, 0, 0, 6}
		if hd == end {
			radii = draw.Radii{0, 6, 6, 0}
		}
		l.Rect(hr, hc, radii)
		grip := pal.Background.WithAlpha(0xb0)
		l.Rect(draw.Rect{X: hr.X + hr.W/2 - 2.5, Y: hr.Y + hr.H/2 - 8, W: 1.5, H: 16}, grip, draw.Uniform(0.75))
		l.Rect(draw.Rect{X: hr.X + hr.W/2 + 1, Y: hr.Y + hr.H/2 - 8, W: 1.5, H: 16}, grip, draw.Uniform(0.75))
	}
}

// times draws where each end is under its handle, kept apart and inside
// the window.
func (v *View) times(c *ui.Ctx, l *draw.List, w, y float32) {
	pal := c.Pal
	t := v.hits.Texts()
	a := t.Lay(c, Stamp(v.in), text.MonoRegular, 11.5)
	b := t.Lay(c, Stamp(v.out), text.MonoRegular, 11.5)
	ax := max(pad, v.x(v.in)-handleW/2-a.Width/2)
	bx := min(w-pad-b.Width, v.x(v.out)+handleW/2-b.Width/2)
	if over := ax + a.Width + 12 - bx; over > 0 {
		ax, bx = ax-over/2, bx+over/2
		if ax < pad {
			ax, bx = pad, pad+a.Width+12
		}
		if bx+b.Width > w-pad {
			bx, ax = w-pad-b.Width, w-pad-b.Width-12-a.Width
		}
	}
	controls.Put(l, a, ax, y, pal.TextMuted)
	controls.Put(l, b, bx, y, pal.TextMuted)
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
