// Package player is the player screen: one recording playing in the main
// window, with play and pause, where it is of how long, a bar to move
// along it, its sound on or off, and what can be done with it (copy it,
// trim it, show it in Finder). Pure: it draws a State and calls Actions;
// the app owns both, and the view owns a drag along the bar.
package player

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
	// first frame comes.
	Width, Height float64
	// Frame is the picture shown (0: none yet).
	Frame draw.ImageID
	// Time is where it's playing, and Playing whether it is.
	Time    time.Duration
	Playing bool
	Muted   bool
	// Copied: the recording is on the clipboard.
	Copied bool
	// Error is the last thing that went wrong.
	Error string
}

// Actions are what the screen does. Any may be nil (drawn disabled).
type Actions struct {
	Play, Pause func()
	// Seek moves to t: exactly (a key, the end of a drag), or to a frame
	// near it that's quick to show (along a drag).
	Seek     func(t time.Duration, exact bool)
	SetMuted func(muted bool)
	// Copy puts the recording on the clipboard, Trim opens the trim
	// screen on it, Reveal shows it in Finder.
	Copy, Trim, Reveal func()
	Back               func()
}

// View draws a State and routes input to Actions.
type View struct {
	St  State
	Act Actions

	hits   *controls.Hits
	pic    draw.Rect     // the picture, as last drawn
	bar    draw.Rect     // the bar's line, as last drawn
	drag   bool          // the bar is being dragged
	resume bool          // it was playing when the drag began
	dragAt time.Duration // where the drag is
	// The pointer, for the time over the bar: where it is, whether it's
	// in the window, and whether it was over the bar.
	ptr     draw.Point
	in      bool
	wasOver bool
}

// New returns a view of st.
func New(st State) *View { return &View{St: st, hits: controls.NewHits()} }

const (
	pad      = 20
	bodySize = 13
	rowH     = 40 // the controls under the picture
	playSize = 40
	btn      = 32
	lineH    = 4 // the bar's line; on hover, lineHot
	lineHot  = 6
	knob     = 12
	// Skip is how far an arrow key moves.
	Skip = 5 * time.Second
)

// Draw lays out and draws the screen at w×h.
func (v *View) Draw(c *ui.Ctx, l *draw.List, w, h float32) {
	v.hits.Begin()
	pal := c.Pal
	t := v.hits.Texts()
	st := &v.St

	// Back to the recordings, and what this is.
	v.back(c, l, 10)
	title := st.Title
	if title == "" {
		title = st.Name
	}
	tl := t.Lay(c, title, text.SansSemibold, 14)
	tw := min(tl.Width, w-2*150)
	t.PutFit(c, l, title, text.SansSemibold, 14, (w-tw)/2, 26-tl.Height/2, tw, pal.Text)

	// The controls along the bottom, and the picture as large as fits
	// above them; a click on it plays or pauses.
	row := draw.Rect{X: pad, Y: h - pad - rowH, W: w - 2*pad, H: rowH}
	box := draw.Rect{X: pad, Y: 52, W: w - 2*pad, H: row.Y - 16 - 52}
	pic := fit(box, st.Width, st.Height)
	v.pic = pic
	l.Rect(box, pal.Surface, draw.Uniform(12))
	if st.Frame != 0 {
		l.Image(pic, st.Frame, draw.Uniform(min(12, pic.W/2)), 255)
	} else {
		msg, col := "Loading…", pal.TextMuted
		if st.Error != "" {
			msg, col = st.Error, pal.Error
		}
		lay := t.Lay(c, msg, text.SansRegular, bodySize)
		t.PutFit(c, l, msg, text.SansRegular, bodySize, box.X+max(16, (box.W-lay.Width)/2), box.Y+(box.H-lay.Height)/2, box.W-32, col)
	}
	if st.Error != "" && st.Frame != 0 {
		// Over the bottom of the picture.
		lay := t.Lay(c, st.Error, text.SansSemibold, bodySize)
		ew := min(lay.Width+32, pic.W-24)
		er := draw.Rect{X: pic.X + (pic.W-ew)/2, Y: pic.Bottom() - 12 - 32, W: ew, H: 32}
		l.Rect(er, pal.Background.WithAlpha(0xe0), draw.Uniform(16))
		t.PutFit(c, l, st.Error, text.SansSemibold, bodySize, er.X+16, er.Y+(er.H-lay.Height)/2, er.W-32, pal.Error)
	}
	v.hits.Area(pic, v.toggle)
	v.controls(c, l, row)
	v.hits.Tip(c, l, w, h)
}

// back draws "‹ Recordings" at the top left: Back.
func (v *View) back(c *ui.Ctx, l *draw.List, y float32) {
	pal := c.Pal
	lay := v.hits.Texts().Lay(c, "Recordings", text.SansRegular, bodySize)
	r := draw.Rect{X: pad - 8, Y: y, W: lay.Width + 38, H: 32}
	col := pal.TextMuted
	switch {
	case v.hits.AreaTip(r, "Back to the recordings (Esc)", v.Act.Back != nil, v.Act.Back):
		col = pal.Text
		l.Rect(r, pal.Text.WithAlpha(0x16), draw.Uniform(r.H/2))
	case v.Act.Back == nil:
		col = pal.TextFaint
	}
	icons.ChevronLeft(l, r.X+16, r.Y+r.H/2, 16, col)
	controls.Put(l, lay, r.X+28, r.Y+(r.H-lay.Height)/2, col)
}

// controls draws, in row: play or pause, where it is of how long, the
// bar, the sound, and what can be done with the recording.
func (v *View) controls(c *ui.Ctx, l *draw.List, row draw.Rect) {
	pal := c.Pal
	t := v.hits.Texts()
	st := &v.St
	mid := row.Y + row.H/2

	pr := draw.Rect{X: row.X, Y: mid - playSize/2, W: playSize, H: playSize}
	icon, tip, do := icons.Play, "Play (Space)", v.Act.Play
	if st.Playing {
		icon, tip, do = icons.Pause, "Pause (Space)", v.Act.Pause
	}
	v.hits.IconButton(c, l, pr, icon, tip, controls.IconOpts{Fill: pal.SurfaceAlt, Size: 18}, do != nil && st.Duration > 0, do)

	now := v.Shown()
	at := Clock(now)
	if now >= st.Duration && st.Duration > 0 {
		at = Length(st.Duration) // the end reads as the length
	}
	clock := t.Lay(c, at+" / "+Length(st.Duration), text.MonoRegular, 12)
	cx := pr.Right() + 14
	controls.Put(l, clock, cx, mid-clock.Height/2, pal.TextMuted)

	// From the right: show in Finder, trim, copy; a rule; the sound.
	cp := action{icons.Copy, "Copy", v.Act.Copy}
	if st.Copied {
		cp.icon, cp.tip = icons.Check, "Copied"
	}
	x := row.Right() - btn
	for _, a := range []action{{icons.Reveal, "Show in Finder", v.Act.Reveal}, {icons.Trim, "Trim…", v.Act.Trim}, cp} {
		v.hits.IconButton(c, l, draw.Rect{X: x, Y: mid - btn/2, W: btn, H: btn}, a.icon, a.tip, controls.IconOpts{Size: 16}, a.do != nil, a.do)
		x -= btn + 4
	}
	x += btn + 4 - 9
	l.Rect(draw.Rect{X: x, Y: mid - 12, W: 1, H: 24}, pal.Border, draw.Radii{})
	x -= 8 + btn
	sound, soundTip, soundCol := icons.Speaker, "Sound: on (M)", draw.Color{}
	if st.Muted {
		sound, soundTip, soundCol = icons.SpeakerOff, "Sound: off (M)", pal.TextMuted
	}
	v.hits.IconButton(c, l, draw.Rect{X: x, Y: mid - btn/2, W: btn, H: btn}, sound, soundTip, controls.IconOpts{Color: soundCol, Size: 17},
		v.Act.SetMuted != nil, func() { v.Act.SetMuted(!st.Muted) })

	// The bar, between the time and the sound.
	v.bar = draw.Rect{X: cx + clock.Width + 18 + knob/2, Y: mid - lineH/2, W: x - 14 - knob/2 - (cx + clock.Width + 18 + knob/2), H: lineH}
	v.drawBar(c, l, now)
}

// action is one of the recording's buttons.
type action struct {
	icon icons.Icon
	tip  string
	do   func()
}

// drawBar draws the bar: the part played, a knob where it is, and over
// the pointer, the time there.
func (v *View) drawBar(c *ui.Ctx, l *draw.List, now time.Duration) {
	pal := c.Pal
	r := v.bar
	if r.W <= 0 {
		return
	}
	hot := v.drag || (v.in && v.grip().Contains(v.ptr) && v.St.Duration > 0)
	line := r
	if hot {
		line = draw.Rect{X: r.X, Y: r.Y - (lineHot-lineH)/2, W: r.W, H: lineHot}
	}
	l.Rect(line, pal.Text.WithAlpha(0x2e), draw.Uniform(line.H/2))
	kx := v.x(now)
	if kx > line.X {
		l.Rect(draw.Rect{X: line.X, Y: line.Y, W: kx - line.X, H: line.H}, pal.Text, draw.Uniform(line.H/2))
	}
	k := float32(knob)
	if hot {
		k += 2
	}
	kr := draw.Rect{X: kx - k/2, Y: r.Y + r.H/2 - k/2, W: k, H: k}
	l.Shadow(kr, pal.Shadow, draw.Uniform(k/2), 6)
	l.Rect(kr, pal.Text, draw.Uniform(k/2))

	if hot {
		px := v.ptr.X
		if v.drag {
			px = kx
		}
		lay := v.hits.Texts().Lay(c, Clock(v.at(px)), text.MonoRegular, 11.5)
		tr := draw.Rect{X: px - lay.Width/2 - 8, Y: r.Y - 14 - 22, W: lay.Width + 16, H: 22}
		l.Shadow(tr, pal.Shadow, draw.Uniform(6), 10)
		l.BorderedRect(tr, pal.SurfaceAlt, pal.Border, 1, draw.Uniform(6))
		controls.Put(l, lay, tr.X+8, tr.Y+(tr.H-lay.Height)/2, pal.Text)
	}
}

// grip is where a press takes the bar: its line, grown to be easy to hit.
func (v *View) grip() draw.Rect {
	r := v.bar
	return draw.Rect{X: r.X - knob, Y: r.Y + r.H/2 - 12, W: r.W + 2*knob, H: 24}
}

// Shown is the time the screen shows: the drag's while dragging.
func (v *View) Shown() time.Duration {
	if v.drag {
		return v.dragAt
	}
	return v.St.Time
}

// x is where time d is on the bar.
func (v *View) x(d time.Duration) float32 {
	if v.St.Duration <= 0 {
		return v.bar.X
	}
	f := float32(float64(min(max(d, 0), v.St.Duration)) / float64(v.St.Duration))
	return v.bar.X + v.bar.W*f
}

// at is the time at x on the bar.
func (v *View) at(x float32) time.Duration {
	if v.bar.W <= 0 {
		return 0
	}
	f := min(max(float64((x-v.bar.X)/v.bar.W), 0), 1)
	return time.Duration(f * float64(v.St.Duration))
}

// toggle plays or pauses.
func (v *View) toggle() {
	if v.St.Playing {
		call0(v.Act.Pause)
	} else {
		call0(v.Act.Play)
	}
}

// seek moves to d, inside the recording.
func (v *View) seek(d time.Duration, exact bool) {
	d = min(max(d, 0), v.St.Duration)
	if v.Act.Seek != nil {
		v.Act.Seek(d, exact)
	}
}

// Event handles input and reports whether to redraw and whether the
// pointer is over something clickable.
func (v *View) Event(c *ui.Ctx, ev ui.Event) (redraw, hand bool) {
	switch ev.Kind {
	case ui.PointerMove, ui.PointerDown, ui.PointerUp:
		v.ptr, v.in = ev.Pos, true
	case ui.PointerLeave:
		v.in = false
	}
	st := &v.St
	switch ev.Kind {
	case ui.KeyPress:
		switch {
		case ev.Key == ui.KeyEscape && v.Act.Back != nil:
			v.drag = false
			v.Act.Back()
			return true, false
		case ev.Key == ' ' && st.Duration > 0:
			v.toggle()
			return true, false
		case ev.Key == 'm' && v.Act.SetMuted != nil:
			v.Act.SetMuted(!st.Muted)
			return true, false
		case (ev.Key == ui.KeyArrowLeft || ev.Key == ui.KeyArrowRight) && !v.drag:
			d := Skip
			if ev.Key == ui.KeyArrowLeft {
				d = -d
			}
			v.seek(st.Time+d, true)
			return true, false
		case ev.Key == ui.KeyHome && !v.drag:
			v.seek(0, true)
			return true, false
		}
	case ui.PointerDown:
		if ev.Button == 1 && st.Duration > 0 && v.Act.Seek != nil && v.grip().Contains(ev.Pos) {
			v.drag, v.resume = true, st.Playing
			if st.Playing {
				call0(v.Act.Pause)
			}
			v.dragAt = v.at(ev.Pos.X)
			v.seek(v.dragAt, false)
			return true, true
		}
	case ui.PointerMove:
		if v.drag {
			v.dragAt = v.at(ev.Pos.X)
			v.seek(v.dragAt, false)
			return true, true
		}
	case ui.PointerUp:
		if v.drag {
			v.drag = false
			v.seek(v.dragAt, true)
			// Shown until the player is there.
			st.Time = v.dragAt
			if v.resume {
				call0(v.Act.Play)
			}
			return true, true
		}
	}
	r, _, hand := v.hits.Event(ev)
	over := v.in && st.Duration > 0 && v.grip().Contains(v.ptr)
	if ev.Kind == ui.PointerMove && (over || v.wasOver) {
		r = true // the time over the bar follows the pointer
	}
	v.wasOver = over
	return r, hand || over
}

// Dragging reports whether the bar is being dragged.
func (v *View) Dragging() bool { return v.drag }

// Hover puts the pointer at p, for renders.
func (v *View) Hover(p draw.Point) { v.ptr, v.in = p, true }

// Picture is where the picture was drawn last frame: the frames to ask
// for are its size in pixels.
func (v *View) Picture() draw.Rect { return v.pic }

// Bar is where the bar's line was drawn last frame (renders, tests).
func (v *View) Bar() draw.Rect { return v.bar }

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

// Clock writes where it's playing as m:ss (h:mm:ss past an hour), the
// seconds gone by.
func Clock(d time.Duration) string { return clock(int(d / time.Second)) }

// Length writes how long it lasts as Clock does, rounded as a card's is.
func Length(d time.Duration) string { return clock(int(d.Round(time.Second) / time.Second)) }

func clock(s int) string {
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

func call0(fn func()) {
	if fn != nil {
		fn()
	}
}
