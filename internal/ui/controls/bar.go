package controls

// The record bar's controls, in the floating controls' style: round icon
// buttons, chips that open a menu, a row of icons to choose between, and
// the tooltips that name them.

import (
	"time"

	"github.com/anthonyrego/reel/internal/draw"
	"github.com/anthonyrego/reel/internal/text"
	"github.com/anthonyrego/reel/internal/ui"
	"github.com/anthonyrego/reel/internal/ui/icons"
)

// hoverAlpha is the white wash under the pointer (the floating controls').
const hoverAlpha = 0x22

// IconOpts is how an icon button looks besides its icon.
type IconOpts struct {
	// Base is drawn under the button always (over a picture); zero: none.
	Base draw.Color
	// Fill is drawn under the icon always (a toggle that's on); zero: none.
	Fill draw.Color
	// Color is the icon's; zero: the text color.
	Color draw.Color
	// Size is the icon's; zero: half the button.
	Size float32
}

// IconButton draws a round button showing icon in r (a square), records
// it with its tooltip and reports whether the pointer is over it.
func (h *Hits) IconButton(c *ui.Ctx, l *draw.List, r draw.Rect, icon icons.Icon, tip string, o IconOpts, enabled bool, do func()) bool {
	pal := c.Pal
	hover := h.addTip(r, enabled, do, tip)
	round := draw.Uniform(min(r.W, r.H) / 2)
	if o.Base.A != 0 {
		l.Rect(r, o.Base, round)
	}
	if o.Fill.A != 0 {
		l.Rect(r, o.Fill, round)
	}
	if hover {
		l.Rect(r, pal.Text.WithAlpha(hoverAlpha), round)
	}
	col := o.Color
	if col.A == 0 {
		col = pal.Text
	}
	if !enabled {
		col = pal.TextFaint
	}
	size := o.Size
	if size == 0 {
		size = r.W / 2
	}
	icon(l, r.X+r.W/2, r.Y+r.H/2, size, col)
	return hover
}

// IconSegment is one choice of a row of icons.
type IconSegment struct {
	Value, Tip string
	Icon       icons.Icon
}

// IconSegmented draws a row of round icon buttons, size across, from x and
// centered on mid; the one equal to cur is filled. A click picks one. It
// returns the row's rect.
func (h *Hits) IconSegmented(c *ui.Ctx, l *draw.List, x, mid, size float32, cur string, enabled bool, segs []IconSegment, pick func(string)) draw.Rect {
	pal := c.Pal
	const gap = 2
	row := draw.Rect{X: x, Y: mid - size/2, W: float32(len(segs))*(size+gap) - gap, H: size}
	for i, sg := range segs {
		r := draw.Rect{X: x + float32(i)*(size+gap), Y: row.Y, W: size, H: size}
		v := sg.Value
		if v == cur {
			// The choice: filled, and nothing to click (but its tooltip).
			h.addTip(r, false, nil, sg.Tip)
			l.Rect(r, pal.SurfaceAlt, draw.Uniform(size/2))
			col := pal.Text
			if !enabled {
				col = pal.TextMuted
			}
			sg.Icon(l, r.X+r.W/2, r.Y+r.H/2, size*0.5, col)
			continue
		}
		h.IconButton(c, l, r, sg.Icon, sg.Tip, IconOpts{Color: pal.TextMuted, Size: size * 0.5}, enabled, func() { pick(v) })
	}
	return row
}

// Chip layout.
const (
	chipPad   = 12
	chipIcon  = 16
	chipLabel = 13
	chipGap   = 8
	chevronW  = 18
)

// ChipWidth is how wide Chip needs to be to show its label and detail
// whole.
func (h *Hits) ChipWidth(c *ui.Ctx, icon bool, label, detail string) float32 {
	w := float32(chipPad) + h.texts.Lay(c, label, text.SansSemibold, chipLabel).Width + chevronW + chipPad
	if icon {
		w += chipIcon + chipGap
	}
	if detail != "" {
		w += 8 + h.texts.Lay(c, detail, text.SansRegular, 12).Width
	}
	return w
}

// Chip draws a dropdown in a bar at r: an icon (nil: none), the label (or
// the placeholder, muted, when label is empty), a muted detail when it
// fits whole, and a chevron. A click runs open.
func (h *Hits) Chip(c *ui.Ctx, l *draw.List, r draw.Rect, icon icons.Icon, label, detail, placeholder, tip string, enabled bool, open func()) {
	pal := c.Pal
	if h.addTip(r, enabled, open, tip) {
		l.Rect(r, pal.Text.WithAlpha(hoverAlpha*2/3), draw.Uniform(r.H/2))
	}
	col, dcol, icol := pal.Text, pal.TextMuted, pal.TextMuted
	if label == "" {
		label, col = placeholder, pal.TextMuted
	}
	if !enabled {
		col, dcol, icol = pal.TextFaint, pal.TextFaint, pal.TextFaint
	}
	mid := r.Y + r.H/2
	x := r.X + chipPad
	if icon != nil {
		icon(l, x+chipIcon/2, mid, chipIcon, icol)
		x += chipIcon + chipGap
	}
	room := r.Right() - chipPad - chevronW - x
	lay := h.texts.Lay(c, label, text.SansSemibold, chipLabel)
	h.texts.PutFit(c, l, label, text.SansSemibold, chipLabel, x, mid-lay.Height/2, room, col)
	if d := h.texts.Lay(c, detail, text.SansRegular, 12); detail != "" && room-lay.Width-8 >= d.Width {
		// Whole or not at all.
		Put(l, d, x+lay.Width+8, mid-d.Height/2+0.5, dcol)
	}
	icons.ChevronDown(l, r.Right()-chipPad-chevronW/2+4, mid, 14, icol)
}

// tipDelay is how long the pointer rests on a control before its tooltip
// shows.
const tipDelay = 400 * time.Millisecond

// Tip draws the tooltip of the control under the pointer once it has
// rested there tipDelay (asking for frames until then) in a window w×h.
// Call it after everything else.
func (h *Hits) Tip(c *ui.Ctx, l *draw.List, w, ht float32) {
	at := -1
	if h.inside && !h.tipHidden {
		for i := len(h.hits) - 1; i >= 0; i-- {
			if h.hits[i].r.Contains(h.pos) {
				at = i
				break
			}
		}
	}
	if at < 0 || h.hits[at].tip == "" {
		h.tipAt = tipKey{}
		return
	}
	hit := h.hits[at]
	if k := (tipKey{hit.tip, hit.r}); k != h.tipAt {
		h.tipAt, h.tipSince = k, c.Now
	}
	if wait := tipDelay - c.Now.Sub(h.tipSince); wait > 0 {
		c.RequestFrame()
		return
	}
	pal := c.Pal
	lay := h.texts.Lay(c, hit.tip, text.SansRegular, 12)
	box := draw.Rect{W: lay.Width + 16, H: lay.Height + 10}
	box.X = max(8, min(hit.r.X+hit.r.W/2-box.W/2, w-8-box.W))
	box.Y = hit.r.Bottom() + 6
	if box.Bottom() > ht-8 {
		box.Y = hit.r.Y - 6 - box.H
	}
	l.Shadow(box, pal.Shadow, draw.Uniform(6), 10)
	l.BorderedRect(box, pal.SurfaceAlt, pal.Border, 1, draw.Uniform(6))
	Put(l, lay, box.X+8, box.Y+5, pal.Text)
}

// TipShown reports whether a tooltip is showing or waiting to (tests).
func (h *Hits) TipShown(now time.Time) bool {
	return h.tipAt.tip != "" && now.Sub(h.tipSince) >= tipDelay
}
