// Package home is reel's main window: what to record (a display, a
// window or an area), the sound (a microphone and system audio), the
// Record button, and the recordings, newest first, each a click away from
// the clipboard. Pure: it draws a State and calls Actions; the app owns
// both.
package home

import (
	"fmt"
	"math"
	"time"

	"github.com/anthonyrego/reel/internal/draw"
	"github.com/anthonyrego/reel/internal/text"
	"github.com/anthonyrego/reel/internal/ui"
	"github.com/anthonyrego/reel/internal/ui/controls"
)

// Target is what to record.
type Target string

const (
	Display Target = "display"
	Window  Target = "window"
	Area    Target = "area"
)

// Perm is a permission's state.
type Perm int

const (
	PermOK     Perm = iota
	PermAsk         // not asked yet: a button asks
	PermDenied      // denied: a button opens System Settings
)

// Recording is a finished recording in the list.
type Recording struct {
	Path     string
	Name     string
	Duration time.Duration
	Size     int64
	When     time.Time
}

// Live is a recording in progress.
type Live struct {
	Elapsed time.Duration
	Paused  bool
	// Hidden: the recorded window is off screen, so the picture holds
	// still.
	Hidden bool
}

// NoMic is the microphone choice that records none.
const NoMic = ""

// State is everything the window shows.
type State struct {
	Screen, Mic Perm

	Target   Target
	Displays []controls.Choice
	Display  string
	Windows  []controls.Choice
	Window   string
	// AreaLabel describes the chosen area ("" before one is chosen).
	AreaLabel string

	Mics        []controls.Choice
	MicID       string // NoMic for none
	SystemAudio bool
	// Warning is a line about the sound (echo, a Bluetooth mic).
	Warning string

	// HotKey names the global key that starts and stops ("⌃⌥R").
	HotKey string
	// Live is the recording in progress (nil: none); Finishing is set
	// while the last one is being written out.
	Live      *Live
	Finishing bool
	// Error is the last thing that went wrong, shown until the next try.
	Error string

	Recordings []Recording
	// Copied is the recording on the clipboard.
	Copied string
	// Toast is a short message at the bottom, fading from ToastAt.
	Toast   string
	ToastAt time.Time
}

// Actions are what the window's controls do. Any may be nil (the control
// is drawn disabled).
type Actions struct {
	AskScreen, AskMic func()
	SetTarget         func(Target)
	PickDisplay       func(id string)
	PickWindow        func(id string)
	SelectArea        func()
	PickMic           func(id string)
	SetSystemAudio    func(on bool)
	Record            func()
	Pause, Resume     func()
	Stop              func()
	Copy, Reveal      func(path string)
	Open, Delete      func(path string)
	Trim              func(path string)
	OpenFolder        func()
}

// View draws a State and routes input to Actions.
type View struct {
	St  State
	Act Actions

	hits    *controls.Hits
	menu    string // the open dropdown: "display", "window", "mic" or ""
	scroll  float32
	listTop float32 // where the recordings list starts, this frame
	listH   float32 // its visible height
	content float32 // its content height
	confirm string  // the recording whose Delete asks again
}

// New returns a view.
func New() *View { return &View{hits: controls.NewHits()} }

const (
	pad       = 28
	rowH      = 34
	recRowH   = 56
	titleSize = 22
	headSize  = 11.5
	bodySize  = 13.5
	smallSize = 12
	fieldH    = 32
	toastFor  = 3 * time.Second
)

// Draw lays out and draws the window at w×h.
func (v *View) Draw(c *ui.Ctx, l *draw.List, w, h float32) {
	v.hits.Begin()
	pal := c.Pal
	t := v.hits.Texts()
	st := &v.St
	y := float32(24)

	// Header: the name, and Record (or the live recording's controls).
	controls.Put(l, t.Lay(c, "Reel", text.SansSemibold, titleSize), pad, y, pal.Text)
	v.header(c, l, w, y)
	y += 50

	if st.Screen != PermOK || st.Mic != PermOK {
		y = v.permissions(c, l, w, y) + 14
	}
	if st.Error != "" {
		lay := c.Text.Layout([]text.Run{{Text: st.Error, Style: text.Style{Font: text.SansRegular, Size: smallSize, Color: pal.Error}}}, w-2*pad)
		controls.Put(l, lay, pad, y, pal.Error)
		y += lay.Height + 14
	}

	// What to record.
	y = v.section(c, l, w, y, "WHAT TO RECORD")
	recording := st.Live != nil || st.Finishing
	v.hits.Segmented(c, l, pad, y+fieldH/2, string(st.Target), !recording,
		[]controls.Segment{{Value: string(Display), Label: "Display"}, {Value: string(Window), Label: "Window"}, {Value: string(Area), Label: "Area"}},
		func(s string) { v.menu = ""; call1(v.Act.SetTarget, Target(s)) })
	fx := pad + float32(260)
	field := draw.Rect{X: fx, Y: y, W: w - pad - fx, H: fieldH}
	switch st.Target {
	case Display:
		label, detail := find(st.Displays, st.Display)
		v.hits.Field(c, l, field, label, detail, "No display", !recording && len(st.Displays) > 0, v.opener("display"))
	case Window:
		label, detail := find(st.Windows, st.Window)
		v.hits.Field(c, l, field, label, detail, "Choose a window", !recording && len(st.Windows) > 0, v.opener("window"))
	case Area:
		label := st.AreaLabel
		bw := v.hits.ButtonWidth(c, "Select…", controls.Secondary)
		if label == "" {
			label = "No area yet"
		}
		lay := t.Lay(c, label, text.SansRegular, bodySize)
		col := pal.Text
		if st.AreaLabel == "" {
			col = pal.TextMuted
		}
		t.PutFit(c, l, label, text.SansRegular, bodySize, field.X, field.Y+(fieldH-lay.Height)/2, field.W-bw-12, col)
		v.hits.Button(c, l, field.Right()-bw, field.Y+2, "Select…", controls.Secondary, !recording && v.Act.SelectArea != nil, v.Act.SelectArea)
	}
	y += fieldH + 26

	// Sound.
	y = v.section(c, l, w, y, "SOUND")
	controls.Put(l, t.Lay(c, "Microphone", text.SansRegular, bodySize), pad, y+(fieldH-17)/2, pal.Text)
	micField := draw.Rect{X: fx, Y: y, W: w - pad - fx, H: fieldH}
	label, detail := find(st.Mics, st.MicID)
	v.hits.Field(c, l, micField, label, detail, "No microphone", !recording && st.Mic == PermOK, v.opener("mic"))
	y += fieldH + 12
	controls.Put(l, t.Lay(c, "System audio", text.SansRegular, bodySize), pad, y+(rowH-17)/2, pal.Text)
	controls.Put(l, t.Lay(c, "what the Mac plays, mixed with the mic", text.SansRegular, smallSize), pad+110, y+(rowH-15)/2+1, pal.TextMuted)
	v.hits.Toggle(c, l, w-pad, y+rowH/2, st.SystemAudio, !recording, func() { call1(v.Act.SetSystemAudio, !st.SystemAudio) })
	y += rowH
	if st.Warning != "" {
		lay := c.Text.Layout([]text.Run{{Text: "⚠  " + st.Warning, Style: text.Style{Font: text.SansRegular, Size: smallSize, Color: pal.Warning}}}, w-2*pad)
		controls.Put(l, lay, pad, y+4, pal.Warning)
		y += lay.Height + 8
	}
	y += 20

	// Recordings.
	y = v.section(c, l, w, y, "RECORDINGS")
	if v.Act.OpenFolder != nil {
		bw := v.hits.ButtonWidth(c, "Open folder", controls.Quiet)
		v.hits.Button(c, l, w-pad-bw+14, y-buttonLift, "Open folder", controls.Quiet, true, v.Act.OpenFolder)
	}
	v.listTop, v.listH = y, max(h-y-16, 0)
	v.recordings(c, l, w)

	v.toast(c, l, w, h)
	v.openMenu(c, l, w, h, field, micField)
}

// buttonLift raises a section header's button to sit on its line.
const buttonLift = 41

// header draws Record, or the live recording's timer and controls, right-
// aligned on the title's line.
func (v *View) header(c *ui.Ctx, l *draw.List, w, y float32) {
	pal := c.Pal
	t := v.hits.Texts()
	st := &v.St
	right := w - pad
	by := y - 1
	switch {
	case st.Finishing:
		lay := t.Lay(c, "Finishing…", text.SansRegular, bodySize)
		controls.Put(l, lay, right-lay.Width, by+6, pal.TextMuted)
	case st.Live != nil:
		bw := v.hits.ButtonWidth(c, "Stop", controls.Primary)
		v.hits.Button(c, l, right-bw, by, "Stop", controls.Primary, v.Act.Stop != nil, v.Act.Stop)
		right -= bw + 8
		label, do := "Pause", v.Act.Pause
		if st.Live.Paused && !st.Live.Hidden {
			label, do = "Resume", v.Act.Resume
		}
		bw = v.hits.ButtonWidth(c, label, controls.Secondary)
		v.hits.Button(c, l, right-bw, by, label, controls.Secondary, do != nil, do)
		right -= bw + 16
		clock := Clock(st.Live.Elapsed)
		col := pal.Error
		switch {
		case st.Live.Hidden:
			clock += "  window hidden"
			col = pal.Warning
		case st.Live.Paused:
			clock += "  paused"
			col = pal.TextMuted
		}
		lay := t.Lay(c, clock, text.SansSemibold, bodySize)
		controls.Put(l, lay, right-lay.Width, by+6, col)
		l.Rect(draw.Rect{X: right - lay.Width - 18, Y: by + 9, W: 10, H: 10}, col, draw.Uniform(5))
	default:
		bw := v.hits.ButtonWidth(c, "Record", controls.Record)
		ok := v.Act.Record != nil && st.Screen == PermOK && (st.Target != Area || st.AreaLabel != "") &&
			(st.Target != Window || st.Window != "")
		v.hits.Button(c, l, right-bw, by, "Record", controls.Record, ok, v.Act.Record)
		if st.HotKey != "" {
			lay := t.Lay(c, st.HotKey, text.SansRegular, smallSize)
			controls.Put(l, lay, right-bw-12-lay.Width, by+7, pal.TextMuted)
		}
	}
}

// permissions draws a card asking for what's missing and returns its
// bottom.
func (v *View) permissions(c *ui.Ctx, l *draw.List, w, y float32) float32 {
	pal := c.Pal
	t := v.hits.Texts()
	st := &v.St
	rows := []struct {
		name, why string
		p         Perm
		ask       func()
	}{
		{"Screen & System Audio Recording", "to record at all", st.Screen, v.Act.AskScreen},
		{"Microphone", "to talk over a recording", st.Mic, v.Act.AskMic},
	}
	n := 0
	for _, r := range rows {
		if r.p != PermOK {
			n++
		}
	}
	card := draw.Rect{X: pad, Y: y, W: w - 2*pad, H: float32(n)*40 + 12}
	l.BorderedRect(card, pal.SurfaceAlt, pal.Border, 1, draw.Uniform(8))
	ry := y + 6
	for _, r := range rows {
		if r.p == PermOK {
			continue
		}
		mid := ry + 20
		lay := t.Lay(c, r.name, text.SansSemibold, bodySize)
		controls.Put(l, lay, card.X+16, mid-lay.Height/2, pal.Text)
		controls.Put(l, t.Lay(c, r.why, text.SansRegular, smallSize), card.X+16+lay.Width+10, mid-7, pal.TextMuted)
		label := "Allow…"
		if r.p == PermDenied {
			label = "Open Settings…"
		}
		bw := v.hits.ButtonWidth(c, label, controls.Primary)
		v.hits.Button(c, l, card.Right()-16-bw, mid-14, label, controls.Primary, r.ask != nil, r.ask)
		ry += 40
	}
	return card.Bottom()
}

// section draws a section's heading and rule and returns where its
// content starts.
func (v *View) section(c *ui.Ctx, l *draw.List, w, y float32, name string) float32 {
	pal := c.Pal
	controls.Put(l, v.hits.Texts().Lay(c, name, text.SansSemibold, headSize), pad, y, pal.TextMuted)
	l.Rect(draw.Rect{X: pad, Y: y + 21, W: w - 2*pad, H: 1}, pal.Border, draw.Radii{})
	return y + 34
}

// recordings draws the list, clipped and scrolled.
func (v *View) recordings(c *ui.Ctx, l *draw.List, w float32) {
	pal := c.Pal
	t := v.hits.Texts()
	st := &v.St
	area := draw.Rect{X: 0, Y: v.listTop, W: w, H: v.listH}
	v.content = float32(len(st.Recordings)) * recRowH
	v.scroll = max(0, min(v.scroll, v.content-v.listH))
	if len(st.Recordings) == 0 {
		controls.Put(l, t.Lay(c, "Nothing yet. Recordings land here, and each is copied to the clipboard when it stops.", text.SansRegular, bodySize),
			pad, v.listTop+4, pal.TextMuted)
		return
	}
	l.PushClip(area)
	defer l.PopClip()
	for i, r := range st.Recordings {
		y := v.listTop + float32(i)*recRowH - v.scroll
		if y+recRowH < v.listTop || y > v.listTop+v.listH {
			continue
		}
		row := draw.Rect{X: pad - 10, Y: y, W: w - 2*pad + 20, H: recRowH - 4}
		path := r.Path
		over := v.hits.Area(row, func() {})
		if over {
			l.Rect(row, pal.SurfaceAlt, draw.Uniform(8))
		}
		// The buttons, right to left.
		right := w - pad
		mid := y + (recRowH-4)/2
		type btn struct {
			label string
			style controls.Style
			do    func()
		}
		var btns []btn
		if v.confirm == path {
			btns = []btn{
				{"Delete", controls.Danger, func() { v.confirm = ""; call1(v.Act.Delete, path) }},
				{"Keep", controls.Secondary, func() { v.confirm = "" }},
			}
		} else {
			copyLabel := "Copy"
			if st.Copied == path {
				copyLabel = "Copied"
			}
			btns = []btn{
				{copyLabel, controls.Secondary, func() { call1(v.Act.Copy, path) }},
				{"Show in Finder", controls.Quiet, func() { call1(v.Act.Reveal, path) }},
			}
			if v.Act.Trim != nil {
				btns = append(btns, btn{"Trim…", controls.Quiet, func() { call1(v.Act.Trim, path) }})
			}
			btns = append(btns, btn{"Delete", controls.Quiet, func() { v.confirm = path }})
		}
		for _, b := range btns {
			bw := v.hits.ButtonWidth(c, b.label, b.style)
			if b.style == controls.Quiet && !over && v.confirm != path {
				right -= bw // drawn only under the pointer, keeping their place
				continue
			}
			v.hits.Button(c, l, right-bw, mid-14, b.label, b.style, true, b.do)
			right -= bw + 4
		}
		// The name and what it is.
		nameW := right - pad - 12
		t.PutFit(c, l, r.Name, text.SansRegular, bodySize, pad, y+9, nameW, pal.Text)
		detail := fmt.Sprintf("%s  ·  %s  ·  %s", Clock(r.Duration), Size(r.Size), When(r.When, c.Now))
		t.PutFit(c, l, detail, text.SansRegular, smallSize, pad, y+29, nameW, pal.TextMuted)
		_ = path
	}
}

// toast draws the message at the bottom while it lasts.
func (v *View) toast(c *ui.Ctx, l *draw.List, w, h float32) {
	st := &v.St
	if st.Toast == "" {
		return
	}
	age := c.Now.Sub(st.ToastAt)
	if age > toastFor {
		return
	}
	alpha := float32(1)
	if fade := toastFor - age; fade < 400*time.Millisecond {
		alpha = float32(fade) / float32(400*time.Millisecond)
	}
	pal := c.Pal
	lay := v.hits.Texts().Lay(c, st.Toast, text.SansSemibold, bodySize)
	r := draw.Rect{X: (w - lay.Width - 36) / 2, Y: h - 64, W: lay.Width + 36, H: 36}
	a := uint8(math.Round(float64(alpha) * 255))
	l.Shadow(r, pal.Shadow.WithAlpha(uint8(float32(pal.Shadow.A)*alpha)), draw.Uniform(18), 16)
	l.Rect(r, pal.Text.WithAlpha(a), draw.Uniform(18))
	controls.Put(l, lay, r.X+18, r.Y+(r.H-lay.Height)/2, pal.Background.WithAlpha(a))
	c.RequestFrame()
}

// openMenu draws the open dropdown over everything.
func (v *View) openMenu(c *ui.Ctx, l *draw.List, w, h float32, target, mic draw.Rect) {
	st := &v.St
	closeMenu := func() { v.menu = "" }
	switch v.menu {
	case "display":
		v.hits.Menu(c, l, target, w, h, st.Displays, st.Display, func(id string) { v.menu = ""; call1(v.Act.PickDisplay, id) }, closeMenu)
	case "window":
		v.hits.Menu(c, l, target, w, h, st.Windows, st.Window, func(id string) { v.menu = ""; call1(v.Act.PickWindow, id) }, closeMenu)
	case "mic":
		v.hits.Menu(c, l, mic, w, h, st.Mics, st.MicID, func(id string) { v.menu = ""; call1(v.Act.PickMic, id) }, closeMenu)
	}
}

func (v *View) opener(name string) func() {
	return func() {
		if v.menu == name {
			v.menu = ""
		} else {
			v.menu = name
		}
	}
}

// Event handles input and reports whether to redraw and whether the
// pointer is over something clickable.
func (v *View) Event(c *ui.Ctx, ev ui.Event) (redraw, hand bool) {
	switch ev.Kind {
	case ui.Wheel:
		if ev.Pos.Y >= v.listTop && v.content > v.listH {
			v.scroll = max(0, min(v.scroll+ev.DY, v.content-v.listH))
			return true, false
		}
	case ui.KeyPress:
		if ev.Key == ui.KeyEscape && (v.menu != "" || v.confirm != "") {
			v.menu, v.confirm = "", ""
			return true, false
		}
	}
	r, _, hand := v.hits.Event(ev)
	return r, hand
}

// Animating reports whether a frame is still moving (the toast).
func (v *View) Animating(now time.Time) bool {
	return v.St.Toast != "" && now.Sub(v.St.ToastAt) < toastFor
}

// find is the label and detail of the choice with id.
func find(cs []controls.Choice, id string) (string, string) {
	for _, ch := range cs {
		if ch.ID == id {
			return ch.Label, ch.Detail
		}
	}
	return "", ""
}

func call1[T any](fn func(T), v T) {
	if fn != nil {
		fn(v)
	}
}

// Clock writes a duration as m:ss (h:mm:ss past an hour).
func Clock(d time.Duration) string {
	s := int(d.Round(time.Second) / time.Second)
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// Size writes a file size in KB, MB or GB.
func Size(n int64) string {
	switch {
	case n >= 1e9:
		return fmt.Sprintf("%.1f GB", float64(n)/1e9)
	case n >= 1e6:
		return fmt.Sprintf("%.1f MB", float64(n)/1e6)
	default:
		return fmt.Sprintf("%d KB", (n+999)/1000)
	}
}

// When writes a time relative to now: a time today, "Yesterday", a
// weekday this week, or a date.
func When(t, now time.Time) string {
	y1, m1, d1 := t.Date()
	y2, m2, d2 := now.Date()
	switch {
	case y1 == y2 && m1 == m2 && d1 == d2:
		return t.Format("3:04 PM")
	case now.Sub(t) < 48*time.Hour && now.AddDate(0, 0, -1).Day() == d1:
		return "Yesterday " + t.Format("3:04 PM")
	case now.Sub(t) < 7*24*time.Hour:
		return t.Format("Monday 3:04 PM")
	case y1 == y2:
		return t.Format("Jan 2")
	}
	return t.Format("Jan 2, 2006")
}

// OpenMenu opens a dropdown ("display", "window", "mic"; "" closes).
func (v *View) OpenMenu(name string) { v.menu = name }
