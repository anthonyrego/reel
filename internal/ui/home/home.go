// Package home is reel's main window: the record bar (what to record, the
// sound, the camera, Record; while recording, the floating controls'
// clock, pause and stop) and the recordings under it as cards, newest first, each a click
// away from playing. Pure: it draws a State and calls Actions; the app
// owns both.
package home

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"time"

	"github.com/anthonyrego/reel/internal/draw"
	"github.com/anthonyrego/reel/internal/text"
	"github.com/anthonyrego/reel/internal/theme"
	"github.com/anthonyrego/reel/internal/ui"
	"github.com/anthonyrego/reel/internal/ui/controls"
	"github.com/anthonyrego/reel/internal/ui/icons"
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
	// Thumb is its picture (0: none yet).
	Thumb draw.ImageID
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

// NoCamera is the camera choice that shows none.
const NoCamera = ""

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
	// Warning is a line about the sound (echo, a Bluetooth mic) or the
	// camera.
	Warning string

	// Cameras are the cameras the bubble can show; CameraID the chosen
	// one (NoCamera: none). Camera is its permission: choosing a camera
	// asks for it.
	Cameras  []controls.Choice
	CameraID string
	Camera   Perm

	// HotKey names the global key that starts and stops ("⌃⌥R").
	HotKey string
	// Live is the recording in progress (nil: none); Starting is set
	// while one starts, Finishing while the last one is being written out.
	Live                *Live
	Starting, Finishing bool
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
	PickCamera        func(id string)
	SetSystemAudio    func(on bool)
	Record            func()
	Pause, Resume     func()
	Stop              func()
	Copy, Reveal      func(path string)
	Open, Delete      func(path string)
	Trim, Play        func(path string)
	OpenFolder        func()
}

// View draws a State and routes input to Actions.
type View struct {
	St  State
	Act Actions

	hits    *controls.Hits
	menu    string    // the open dropdown: "display", "window", "mic", "camera" or ""
	source  draw.Rect // the source chip, this frame (a menu hangs from it)
	mic     draw.Rect // the mic chip
	camera  draw.Rect // the camera chip
	scroll  float32
	listTop float32     // where the recordings start, this frame
	listH   float32     // their visible height
	content float32     // their whole height
	confirm string      // the recording whose Delete asks again
	visible []string    // the recordings drawn, this frame
	cards   []draw.Rect // and where
}

// New returns a view.
func New() *View { return &View{hits: controls.NewHits()} }

const (
	pad       = 20
	barTop    = 6
	barH      = 56
	segSize   = 36
	recordH   = 40
	cardMin   = 200
	gap       = 16
	rowGap    = 18
	textH     = 40 // under a card's picture: the title and the size
	bodySize  = 13
	smallSize = 12
	toastFor  = 3 * time.Second
	// hotKeyFrom is the width from which Record shows the hot key (below
	// it, Record's tooltip does).
	hotKeyFrom = 900
)

// Draw lays out and draws the window at w×h.
func (v *View) Draw(c *ui.Ctx, l *draw.List, w, h float32) {
	v.hits.Begin()
	st := &v.St
	y := v.bar(c, l, w, barTop) + 14

	if st.Error != "" {
		y = v.note(c, l, w, y, st.Error, c.Pal.Error) + 8
	}
	if st.Warning != "" {
		y = v.note(c, l, w, y, st.Warning, c.Pal.Warning) + 8
	}
	if st.Screen != PermOK || st.Mic != PermOK {
		y = v.permissions(c, l, w, y+2) + 10
	}
	y = v.galleryHeader(c, l, w, y+10)
	v.listTop, v.listH = y, max(h-y, 0)
	v.gallery(c, l, w)

	v.toast(c, l, w, h)
	v.openMenu(c, l, w, h)
	v.hits.Tip(c, l, w, h)
}

// bar draws the record bar from y and returns its bottom.
func (v *View) bar(c *ui.Ctx, l *draw.List, w, y float32) float32 {
	pal := c.Pal
	st := &v.St
	recording := st.Live != nil || st.Starting || st.Finishing
	bar := draw.Rect{X: pad, Y: y, W: w - 2*pad, H: barH}
	l.Shadow(bar, pal.Shadow, draw.Uniform(barH/2), 14)
	l.BorderedRect(bar, pal.Bar, pal.Border, 1, draw.Uniform(barH/2))
	mid := bar.Y + barH/2

	// What to record, from the left.
	x := bar.X + (barH-segSize)/2
	segs := []controls.IconSegment{
		{Value: string(Display), Tip: "Record a display", Icon: icons.Display},
		{Value: string(Window), Tip: "Record a window", Icon: icons.Window},
		{Value: string(Area), Tip: "Record an area", Icon: icons.Area},
	}
	row := v.hits.IconSegmented(c, l, x, mid, segSize, string(st.Target), !recording && v.Act.SetTarget != nil, segs,
		func(s string) { v.menu = ""; call1(v.Act.SetTarget, Target(s)) })
	x = row.Right() + 8
	divider(l, x, mid, pal)
	x += 9

	// Record (or the live recording's controls) and system audio, from
	// the right.
	right := v.record(c, l, w, bar.Right()-(barH-recordH)/2, mid) - 10
	sa := draw.Rect{X: right - segSize, Y: mid - segSize/2, W: segSize, H: segSize}
	icon, tip, o := icons.SpeakerOff, "System audio: off", controls.IconOpts{Color: pal.TextMuted, Size: 18}
	if st.SystemAudio {
		icon, tip, o = icons.Speaker, "System audio: on (what the Mac plays, mixed with the mic)", controls.IconOpts{Fill: pal.SurfaceAlt, Size: 18}
	}
	v.hits.IconButton(c, l, sa, icon, tip, o, !recording && v.Act.SetSystemAudio != nil,
		func() { call1(v.Act.SetSystemAudio, !st.SystemAudio) })
	right = sa.X - 4

	// The source, the mic and the camera share what's between: each whole
	// if they fit; else the display's size goes, then the camera and the
	// mic show only their icons (their names in their tooltips), and last
	// the source's label is cut (to no less than about half).
	srcLabel, srcDetail, srcHold, srcOK, srcOpen := v.sourceChip()
	micLabel, _ := find(st.Mics, st.MicID)
	micIcon := icons.Mic
	if st.MicID == NoMic {
		micIcon, micLabel = icons.MicOff, ""
	}
	camLabel, _ := find(st.Cameras, st.CameraID)
	camIcon := icons.Camera
	if st.CameraID == NoCamera {
		camIcon, camLabel = icons.CameraOff, ""
	}
	avail := right - x - 2*9
	srcNat := v.hits.ChipWidth(c, false, labelOr(srcLabel, srcHold), srcDetail)
	srcBare := v.hits.ChipWidth(c, false, labelOr(srcLabel, srcHold), "")
	micNat := v.hits.ChipWidth(c, true, labelOr(micLabel, "No microphone"), "")
	camNat := v.hits.ChipWidth(c, true, labelOr(camLabel, "No camera"), "")
	iconOnly := v.hits.ChipWidth(c, true, "", "")
	srcW, micW, camW := srcNat, micNat, camNat
	fits := func() bool { return srcW+micW+camW <= avail }
	if !fits() {
		srcW = srcBare
	}
	if !fits() {
		camW = iconOnly
	}
	if !fits() {
		micW = iconOnly
	}
	srcW = min(srcW, max(avail-micW-camW, min(srcBare, avail*0.5)))
	srcTip, micTip, camTip := "", "", ""
	if srcW < srcBare && srcLabel != "" {
		srcTip = srcLabel
		if srcDetail != "" {
			srcTip += "  " + srcDetail
		}
	}
	if micW < micNat {
		micTip = labelOr(micLabel, "No microphone")
	}
	if micW == iconOnly {
		micLabel = ""
	}
	camOK := !recording && v.Act.PickCamera != nil && st.Target != Window
	switch {
	case st.Target == Window:
		camTip = "A window recording can't show the camera"
	case camW < camNat:
		camTip = labelOr(camLabel, "No camera")
	case st.Camera == PermDenied && st.CameraID != NoCamera:
		camTip = "Allow Reel in System Settings › Camera"
	}
	camHold := "No camera"
	if camW == iconOnly {
		camLabel, camHold = "", ""
	}
	micHold := "No microphone"
	if micW == iconOnly {
		micHold = ""
	}
	v.source = draw.Rect{X: x, Y: mid - segSize/2, W: srcW, H: segSize}
	v.hits.Chip(c, l, v.source, nil, srcLabel, srcDetail, srcHold, srcTip, !recording && srcOK, srcOpen)
	x = v.source.Right() + 4
	divider(l, x, mid, pal)
	x += 5
	v.mic = draw.Rect{X: x, Y: mid - segSize/2, W: micW, H: segSize}
	v.hits.Chip(c, l, v.mic, micIcon, micLabel, "", micHold, micTip,
		!recording && st.Mic == PermOK && v.Act.PickMic != nil, v.opener("mic"))
	x = v.mic.Right() + 4
	divider(l, x, mid, pal)
	x += 5
	v.camera = draw.Rect{X: x, Y: mid - segSize/2, W: camW, H: segSize}
	v.hits.Chip(c, l, v.camera, camIcon, camLabel, "", camHold, camTip, camOK, v.opener("camera"))
	return bar.Bottom()
}

// sourceChip is what the source chip shows and does for the target: its
// label, detail and placeholder, whether it can be used and what a click
// does.
func (v *View) sourceChip() (label, detail, placeholder string, ok bool, do func()) {
	st := &v.St
	switch st.Target {
	case Window:
		label, detail = find(st.Windows, st.Window)
		return label, detail, "Choose a window", len(st.Windows) > 0 && v.Act.PickWindow != nil, v.opener("window")
	case Area:
		return st.AreaLabel, "", "Select an area…", v.Act.SelectArea != nil, func() { v.menu = ""; call0(v.Act.SelectArea) }
	}
	label, detail = find(st.Displays, st.Display)
	return label, detail, "No display", len(st.Displays) > 0 && v.Act.PickDisplay != nil, v.opener("display")
}

// record draws Record, or the live recording's clock, pause and stop, with
// its right edge at right, and returns its left edge.
func (v *View) record(c *ui.Ctx, l *draw.List, w, right, mid float32) float32 {
	pal := c.Pal
	t := v.hits.Texts()
	st := &v.St
	switch {
	case st.Starting, st.Finishing:
		msg := "Saving…"
		if st.Starting {
			msg = "Starting…"
		}
		lay := t.Lay(c, msg, text.SansSemibold, bodySize)
		controls.Put(l, lay, right-12-lay.Width, mid-lay.Height/2, pal.TextMuted)
		return right - 24 - lay.Width
	case st.Live != nil:
		// The floating controls' own: a dot and the clock, pause, stop.
		const btn = 32
		stop := draw.Rect{X: right - btn, Y: mid - btn/2, W: btn, H: btn}
		v.hits.IconButton(c, l, stop, icons.Stop, "Stop", controls.IconOpts{Color: pal.Live, Size: 15}, v.Act.Stop != nil, v.Act.Stop)
		pause := draw.Rect{X: stop.X - 4 - btn, Y: stop.Y, W: btn, H: btn}
		icon, tip, do := icons.Pause, "Pause", v.Act.Pause
		if st.Live.Paused && !st.Live.Hidden {
			icon, tip, do = icons.Play, "Resume", v.Act.Resume
		}
		v.hits.IconButton(c, l, pause, icon, tip, controls.IconOpts{Size: 15}, do != nil, do)
		dot, label := pal.Live, Clock(st.Live.Elapsed)
		switch {
		case st.Live.Hidden:
			dot, label = pal.Warning, label+"  hidden"
		case st.Live.Paused:
			dot = pal.TextMuted
		}
		lay := t.Lay(c, label, text.SansSemibold, 14)
		x := pause.X - 10 - lay.Width
		controls.Put(l, lay, x, mid-lay.Height/2, pal.Text)
		l.Rect(draw.Rect{X: x - 18, Y: mid - 5, W: 10, H: 10}, dot, draw.Uniform(5))
		return x - 26
	}
	ok, why := v.canRecord()
	label := t.Lay(c, "Record", text.SansSemibold, 14)
	var key text.Layout
	showKey := st.HotKey != "" && w >= hotKeyFrom
	bw := 18 + 10 + 8 + label.Width + 20
	if showKey {
		key = t.Lay(c, st.HotKey, text.SansRegular, smallSize)
		bw += 10 + key.Width
	}
	r := draw.Rect{X: right - bw, Y: mid - recordH/2, W: bw, H: recordH}
	tip := why
	if tip == "" && st.HotKey != "" {
		tip = "Record (" + st.HotKey + " from any app)"
	}
	hover := v.hits.AreaTip(r, tip, ok, v.Act.Record)
	fill, on := pal.Live, controls.OnFill(pal)
	switch {
	case !ok:
		fill, on = pal.SurfaceAlt, pal.TextFaint
	case hover:
		fill = fill.WithAlpha(0xd8)
	}
	l.Rect(r, fill, draw.Uniform(recordH/2))
	x := r.X + 18
	l.Rect(draw.Rect{X: x, Y: mid - 5, W: 10, H: 10}, on, draw.Uniform(5))
	x += 18
	controls.Put(l, label, x, mid-label.Height/2, on)
	if showKey {
		controls.Put(l, key, x+label.Width+10, mid-key.Height/2+0.5, on.WithAlpha(0xa0))
	}
	return r.X
}

// canRecord reports whether Record can start, and if not, why.
func (v *View) canRecord() (bool, string) {
	st := &v.St
	switch {
	case v.Act.Record == nil:
		return false, ""
	case st.Screen != PermOK:
		return false, "Allow Screen Recording first"
	case st.Target == Window && st.Window == "":
		return false, "Choose a window first"
	case st.Target == Area && st.AreaLabel == "":
		return false, "Select an area first"
	}
	return true, ""
}

// note draws a line about the sound or what went wrong, with a warning
// sign, and returns its bottom.
func (v *View) note(c *ui.Ctx, l *draw.List, w, y float32, msg string, col draw.Color) float32 {
	x := float32(pad + 16)
	lay := c.Text.Layout([]text.Run{{Text: msg, Style: text.Style{Font: text.SansRegular, Size: smallSize, Color: col}}}, w-x-22-pad)
	icons.Warning(l, x+7, y+8, 14, col)
	controls.Put(l, lay, x+22, y, col)
	return y + max(lay.Height, 16)
}

// permissions draws a card asking for what's missing and returns its
// bottom.
func (v *View) permissions(c *ui.Ctx, l *draw.List, w, y float32) float32 {
	pal := c.Pal
	t := v.hits.Texts()
	st := &v.St
	rows := []struct {
		icon      icons.Icon
		name, why string
		p         Perm
		ask       func()
	}{
		{icons.Display, "Screen & System Audio Recording", "to record at all", st.Screen, v.Act.AskScreen},
		{icons.Mic, "Microphone", "to talk over a recording", st.Mic, v.Act.AskMic},
	}
	n := 0
	for _, r := range rows {
		if r.p != PermOK {
			n++
		}
	}
	card := draw.Rect{X: pad, Y: y, W: w - 2*pad, H: float32(n)*48 + 8}
	l.BorderedRect(card, pal.Surface, pal.Border, 1, draw.Uniform(14))
	ry := y + 4
	for _, r := range rows {
		if r.p == PermOK {
			continue
		}
		mid := ry + 24
		r.icon(l, card.X+28, mid, 18, pal.TextMuted)
		lay := t.Lay(c, r.name, text.SansSemibold, bodySize)
		controls.Put(l, lay, card.X+50, mid-lay.Height/2, pal.Text)
		label := "Allow…"
		if r.p == PermDenied {
			label = "Open Settings…"
		}
		bw := v.hits.ButtonWidth(c, label, controls.Primary)
		wx := card.X + 50 + lay.Width + 10
		t.PutFit(c, l, r.why, text.SansRegular, smallSize, wx, mid-7, card.Right()-16-bw-12-wx, pal.TextMuted)
		v.hits.Button(c, l, card.Right()-16-bw, mid-14, label, controls.Primary, r.ask != nil, r.ask)
		ry += 48
	}
	return card.Bottom()
}

// galleryHeader draws "Recordings", how many, and Open folder, and returns
// where the cards start.
func (v *View) galleryHeader(c *ui.Ctx, l *draw.List, w, y float32) float32 {
	pal := c.Pal
	t := v.hits.Texts()
	title := t.Lay(c, "Recordings", text.SansSemibold, 15)
	controls.Put(l, title, pad+2, y, pal.Text)
	if n := len(v.St.Recordings); n > 0 {
		controls.Put(l, t.Lay(c, strconv.Itoa(n), text.SansRegular, bodySize), pad+2+title.Width+8, y+1.5, pal.TextMuted)
	}
	if v.Act.OpenFolder != nil {
		lay := t.Lay(c, "Open folder", text.SansRegular, bodySize)
		r := draw.Rect{X: w - pad - lay.Width - 40, Y: y + title.Height/2 - 15, W: lay.Width + 40, H: 30}
		col := pal.TextMuted
		if v.hits.Area(r, v.Act.OpenFolder) {
			col = pal.Text
			l.Rect(r, pal.Text.WithAlpha(0x16), draw.Uniform(r.H/2))
		}
		icons.Folder(l, r.X+20, r.Y+r.H/2, 15, col)
		controls.Put(l, lay, r.X+32, r.Y+(r.H-lay.Height)/2, col)
	}
	return y + title.Height + 12
}

// Columns is how many cards a row holds in a window w wide.
func Columns(w float32) int {
	return max(2, int((w-2*pad+gap)/(cardMin+gap)))
}

// gallery draws the cards, clipped and scrolled.
func (v *View) gallery(c *ui.Ctx, l *draw.List, w float32) {
	st := &v.St
	v.visible, v.cards = v.visible[:0], v.cards[:0]
	if len(st.Recordings) == 0 {
		v.content = 0
		v.empty(c, l, w)
		return
	}
	cols := Columns(w)
	cardW := (w - 2*pad - gap*float32(cols-1)) / float32(cols)
	picH := float32(math.Round(float64(cardW * 10 / 16)))
	rowH := picH + 8 + textH + rowGap
	rows := (len(st.Recordings) + cols - 1) / cols
	// 8 above (a card's hover reaches 6 past it) and 16 below the last.
	v.content = 8 + float32(rows)*rowH - rowGap + 16
	v.scroll = max(0, min(v.scroll, v.content-v.listH))
	area := draw.Rect{X: 0, Y: v.listTop, W: w, H: v.listH}
	l.PushClip(area)
	defer l.PopClip()
	for i, r := range st.Recordings {
		x := pad + float32(i%cols)*(cardW+gap)
		y := v.listTop + 8 + float32(i/cols)*rowH - v.scroll
		card := draw.Rect{X: x, Y: y, W: cardW, H: picH + 8 + textH}
		if card.Bottom() < area.Y || card.Y > area.Bottom() {
			continue
		}
		v.visible, v.cards = append(v.visible, r.Path), append(v.cards, card)
		v.card(c, l, r, card, picH, area)
	}
}

// card draws one recording: its picture with the time it lasts, what it
// is under it, and, under the pointer, what can be done with it.
func (v *View) card(c *ui.Ctx, l *draw.List, r Recording, card draw.Rect, picH float32, area draw.Rect) {
	pal := c.Pal
	t := v.hits.Texts()
	st := &v.St
	path := r.Path
	pic := draw.Rect{X: card.X, Y: card.Y, W: card.W, H: picH}
	over := v.hits.Hovered(card) && v.hits.Hovered(area) && v.menu == ""
	asking := v.confirm == path

	// A click anywhere on the card plays it: recorded first, so its
	// buttons are over it.
	v.hits.Area(card.Intersect(area), func() { call1(v.Act.Play, path) })
	if over {
		l.Rect(draw.Rect{X: card.X - 6, Y: card.Y - 6, W: card.W + 12, H: card.H + 8}, pal.Text.WithAlpha(0x0e), draw.Uniform(14))
	}
	if r.Thumb != 0 {
		l.Image(pic, r.Thumb, draw.Uniform(10), 255)
	} else {
		l.Rect(pic, pal.Surface, draw.Uniform(10))
		icons.Film(l, pic.X+pic.W/2, pic.Y+pic.H/2, 28, pal.TextFaint)
	}
	l.BorderedRect(pic, draw.Color{}, pal.Text.WithAlpha(0x14), 1, draw.Uniform(10))

	// How long it is, in the corner.
	dur := t.Lay(c, Clock(r.Duration), text.SansSemibold, 11.5)
	badge := draw.Rect{X: pic.Right() - 8 - dur.Width - 12, Y: pic.Bottom() - 8 - 20, W: dur.Width + 12, H: 20}
	l.Rect(badge, draw.Color{A: 0xb0}, draw.Uniform(6))
	controls.Put(l, dur, badge.X+6, badge.Y+(badge.H-dur.Height)/2, pal.Text)

	switch {
	case asking:
		l.Rect(pic, pal.Background.WithAlpha(0xe0), draw.Uniform(10))
		q := t.Lay(c, "Move to Trash?", text.SansSemibold, bodySize)
		kw := v.hits.ButtonWidth(c, "Keep", controls.Secondary)
		dw := v.hits.ButtonWidth(c, "Delete", controls.Danger)
		top := pic.Y + (pic.H-q.Height-10-28)/2
		controls.Put(l, q, pic.X+(pic.W-q.Width)/2, top, pal.Text)
		bx := pic.X + (pic.W-kw-8-dw)/2
		v.hits.Button(c, l, bx, top+q.Height+10, "Keep", controls.Secondary, true, func() { v.confirm = "" })
		v.hits.Button(c, l, bx+kw+8, top+q.Height+10, "Delete", controls.Danger, v.Act.Delete != nil,
			func() { v.confirm = ""; call1(v.Act.Delete, path) })
	case over:
		const btn, bgap = 28, 6
		type action struct {
			icon icons.Icon
			tip  string
			do   func(string)
		}
		acts := []action{{icons.Copy, "Copy", v.Act.Copy}}
		if st.Copied == path {
			acts[0] = action{icons.Check, "Copied", v.Act.Copy}
		}
		if v.Act.Trim != nil {
			acts = append(acts, action{icons.Trim, "Trim…", v.Act.Trim})
		}
		acts = append(acts, action{icons.Reveal, "Show in Finder", v.Act.Reveal},
			action{icons.Trash, "Move to Trash", func(p string) { v.confirm = p }})
		x := pic.Right() - 8 - float32(len(acts))*(btn+bgap) + bgap
		for _, a := range acts {
			b := draw.Rect{X: x, Y: pic.Y + 8, W: btn, H: btn}
			do := a.do
			v.hits.IconButton(c, l, b, a.icon, a.tip, controls.IconOpts{Base: draw.Color{A: 0x99}, Size: 15}, do != nil,
				func() { call1(do, path) })
			x += btn + bgap
		}
		// Play, in the middle of the picture, says what a click on the
		// card does.
		if v.Act.Play != nil {
			const play = 48
			b := draw.Rect{X: pic.X + (pic.W-play)/2, Y: pic.Y + (pic.H-play)/2, W: play, H: play}
			v.hits.IconButton(c, l, b, icons.Play, "Play", controls.IconOpts{Base: draw.Color{A: 0x99}, Size: 22}, true,
				func() { call1(v.Act.Play, path) })
		}
	}

	// What it is (when, or its name), how big, whether it's copied.
	ty := pic.Bottom() + 9
	room := card.W - 2
	if st.Copied == path {
		lay := t.Lay(c, "Copied", text.SansSemibold, smallSize)
		cx := card.Right() - lay.Width
		controls.Put(l, lay, cx, ty+1.5, pal.Accent)
		icons.Check(l, cx-9, ty+9, 13, pal.Accent)
		room -= lay.Width + 24
	}
	t.PutFit(c, l, Title(r, c.Now), text.SansSemibold, bodySize, card.X+1, ty, room, pal.Text)
	t.PutFit(c, l, Size(r.Size), text.SansRegular, smallSize, card.X+1, ty+20, card.W-2, pal.TextMuted)
}

// empty draws the gallery with nothing in it.
func (v *View) empty(c *ui.Ctx, l *draw.List, w float32) {
	pal := c.Pal
	t := v.hits.Texts()
	mid := v.listTop + max(60, min(v.listH/2-40, 150))
	icons.Film(l, w/2, mid-34, 36, pal.TextFaint)
	head := t.Lay(c, "No recordings yet", text.SansSemibold, 15)
	controls.Put(l, head, (w-head.Width)/2, mid, pal.Text)
	key := ""
	if v.St.HotKey != "" {
		key = " or " + v.St.HotKey
	}
	msg := c.Text.Layout([]text.Run{{Text: "Press Record" + key + ". Each recording is copied to the clipboard when it stops.",
		Style: text.Style{Font: text.SansRegular, Size: smallSize, Color: pal.TextMuted}}}, min(w-2*pad, 340))
	for _, line := range msg.Lines {
		controls.Put(l, text.Layout{Lines: []text.Line{line}, Width: line.Width}, (w-line.Width)/2, mid+head.Height+8, pal.TextMuted)
	}
}

// toast draws the message at the bottom while it lasts: a pill like the
// floating controls.
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
	r := draw.Rect{X: (w - lay.Width - 40) / 2, Y: h - 64, W: lay.Width + 40, H: 40}
	a := func(col draw.Color) draw.Color {
		return col.WithAlpha(uint8(math.Round(float64(float32(col.A) * alpha))))
	}
	l.Shadow(r, a(pal.Shadow), draw.Uniform(20), 18)
	l.BorderedRect(r, a(pal.SurfaceAlt), a(pal.Border), 1, draw.Uniform(20))
	controls.Put(l, lay, r.X+20, r.Y+(r.H-lay.Height)/2, a(pal.Text))
	c.RequestFrame()
}

// openMenu draws the open dropdown over everything.
func (v *View) openMenu(c *ui.Ctx, l *draw.List, w, h float32) {
	st := &v.St
	closeMenu := func() { v.menu = "" }
	switch v.menu {
	case "display":
		v.hits.Menu(c, l, v.source, w, h, st.Displays, st.Display, func(id string) { v.menu = ""; call1(v.Act.PickDisplay, id) }, closeMenu)
	case "window":
		v.hits.Menu(c, l, v.source, w, h, st.Windows, st.Window, func(id string) { v.menu = ""; call1(v.Act.PickWindow, id) }, closeMenu)
	case "mic":
		v.hits.Menu(c, l, v.mic, w, h, st.Mics, st.MicID, func(id string) { v.menu = ""; call1(v.Act.PickMic, id) }, closeMenu)
	case "camera":
		v.hits.Menu(c, l, v.camera, w, h, st.Cameras, st.CameraID, func(id string) { v.menu = ""; call1(v.Act.PickCamera, id) }, closeMenu)
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
		if v.menu == "" && ev.Pos.Y >= v.listTop && v.content > v.listH {
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
	if ev.Kind == ui.PointerMove && ev.Pos.Y >= v.listTop {
		// A card lights up under the pointer whichever of its parts it's
		// on.
		r = true
	}
	return r, hand
}

// Animating reports whether a frame is still moving (the toast).
func (v *View) Animating(now time.Time) bool {
	return v.St.Toast != "" && now.Sub(v.St.ToastAt) < toastFor
}

// Visible is the recordings drawn last frame, in order: the ones whose
// pictures are wanted first.
func (v *View) Visible() []string { return v.visible }

// Card is where the recording at path was drawn last frame (renders,
// tests).
func (v *View) Card(path string) (draw.Rect, bool) {
	for i, p := range v.visible {
		if p == path {
			return v.cards[i], true
		}
	}
	return draw.Rect{}, false
}

// OpenMenu opens a dropdown ("display", "window", "mic", "camera"; ""
// closes).
func (v *View) OpenMenu(name string) { v.menu = name }

// AskDelete shows a recording's Delete asking again, for renders.
func (v *View) AskDelete(path string) { v.confirm = path }

// divider draws one of the bar's short vertical rules at x.
func divider(l *draw.List, x, mid float32, pal theme.Palette) {
	l.Rect(draw.Rect{X: x, Y: mid - 12, W: 1, H: 24}, pal.Border, draw.Radii{})
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

func labelOr(label, placeholder string) string {
	if label == "" {
		return placeholder
	}
	return label
}

func call0(fn func()) {
	if fn != nil {
		fn()
	}
}

func call1[T any](fn func(T), v T) {
	if fn != nil {
		fn(v)
	}
}

// defaultName is the name library.NewPath gives a recording (perhaps
// numbered) and library.TrimmedPath a trim of one.
var defaultName = regexp.MustCompile(`^Recording \d{4}-\d{2}-\d{2} at \d{2}\.\d{2}\.\d{2}( \d+)?( trimmed( \d+)?)?$`)

// Title is what a card calls a recording: when it was made for the names
// reel gives (marked when it's a trim), else its name.
func Title(r Recording, now time.Time) string {
	m := defaultName.FindStringSubmatch(r.Name)
	switch {
	case m == nil:
		return r.Name
	case m[2] != "":
		return When(r.When, now) + " · Trimmed"
	}
	return When(r.When, now)
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
