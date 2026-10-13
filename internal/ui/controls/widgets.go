package controls

import (
	"github.com/anthonyrego/reel/internal/draw"
	"github.com/anthonyrego/reel/internal/text"
	"github.com/anthonyrego/reel/internal/ui"
)

// Style is how a button looks.
type Style int

const (
	Secondary Style = iota // bordered, on the surface
	Primary                // filled with the accent
	Danger                 // bordered, red label
	Record                 // filled red, with a white dot: starts a recording
	Quiet                  // a label that darkens under the pointer
)

const (
	buttonH   = 28
	buttonPad = 14
	labelSize = 13
)

// ButtonWidth is how wide Button draws label.
func (h *Hits) ButtonWidth(c *ui.Ctx, label string, s Style) float32 {
	w := h.texts.Lay(c, label, text.SansSemibold, labelSize).Width + 2*buttonPad
	if s == Record {
		w += 16
	}
	return w
}

// Button draws a button with its top-left corner at x, y, records it and
// returns its rect.
func (h *Hits) Button(c *ui.Ctx, l *draw.List, x, y float32, label string, s Style, enabled bool, do func()) draw.Rect {
	pal := c.Pal
	lay := h.texts.Lay(c, label, text.SansSemibold, labelSize)
	r := draw.Rect{X: x, Y: y, W: h.ButtonWidth(c, label, s), H: buttonH}
	hover := h.add(r, enabled, do)
	ty := r.Y + (r.H-lay.Height)/2
	tx := r.X + buttonPad
	switch {
	case !enabled:
		l.BorderedRect(r, pal.Surface, pal.Border, 1, draw.Uniform(6))
		Put(l, lay, tx, ty, pal.TextFaint)
	case s == Primary || s == Record:
		fill := pal.Accent
		if s == Record {
			fill = pal.Error
		}
		if hover {
			fill = fill.WithAlpha(0xd8)
		}
		l.Rect(r, fill, draw.Uniform(6))
		if s == Record {
			l.Rect(draw.Rect{X: tx, Y: r.Y + r.H/2 - 5, W: 10, H: 10}, pal.Surface, draw.Uniform(5))
			tx += 16
		}
		Put(l, lay, tx, ty, pal.Surface)
	case s == Quiet:
		col := pal.TextMuted
		if hover {
			col = pal.Text
		}
		Put(l, lay, tx, ty, col)
	default:
		fill := pal.Surface
		if hover {
			fill = pal.SurfaceAlt
		}
		col := pal.Text
		if s == Danger {
			col = pal.Error
		}
		l.BorderedRect(r, fill, pal.Border, 1, draw.Uniform(6))
		Put(l, lay, tx, ty, col)
	}
	return r
}

// Toggle draws an on/off switch right-aligned at right and centered on
// mid, and records it: a click runs do.
func (h *Hits) Toggle(c *ui.Ctx, l *draw.List, right, mid float32, on, enabled bool, do func()) draw.Rect {
	pal := c.Pal
	r := draw.Rect{X: right - 34, Y: mid - 10, W: 34, H: 20}
	hover := h.add(r, enabled, do)
	track, knob := pal.Border, r.X+2
	if on {
		track, knob = pal.Accent, r.Right()-18
	}
	if hover {
		track = track.WithAlpha(0xd8)
	}
	if !enabled {
		track = track.WithAlpha(0x60)
	}
	l.Rect(r, track, draw.Uniform(10))
	l.Rect(draw.Rect{X: knob, Y: r.Y + 2, W: 16, H: 16}, pal.Surface, draw.Uniform(8))
	return r
}

// Segment is one choice of a segmented control.
type Segment struct {
	Value, Label string
}

// Segmented draws a row of choices, the one equal to cur raised, with its
// left edge at x and centered on mid, and records each: a click picks it.
// It returns the control's rect.
func (h *Hits) Segmented(c *ui.Ctx, l *draw.List, x, mid float32, cur string, enabled bool, segs []Segment, pick func(string)) draw.Rect {
	pal := c.Pal
	const ht, pad = 28, 16
	w := float32(0)
	lays := make([]text.Layout, len(segs))
	for i, sg := range segs {
		lays[i] = h.texts.Lay(c, sg.Label, text.SansRegular, labelSize)
		w += lays[i].Width + 2*pad
	}
	track := draw.Rect{X: x, Y: mid - ht/2, W: w, H: ht}
	l.Rect(track, pal.SurfaceAlt, draw.Uniform(7))
	for i, sg := range segs {
		r := draw.Rect{X: x, Y: track.Y, W: lays[i].Width + 2*pad, H: ht}
		x = r.Right()
		v := sg.Value
		hover := h.add(r, enabled && v != cur, func() { pick(v) })
		col := pal.TextMuted
		switch {
		case v == cur:
			l.BorderedRect(r.Inset(2), pal.Surface, pal.Border, 1, draw.Uniform(5))
			col = pal.Text
		case !enabled:
			col = pal.TextFaint
		case hover:
			col = pal.Text
		}
		Put(l, lays[i], r.X+(r.W-lays[i].Width)/2, mid-lays[i].Height/2, col)
	}
	return track
}

// Choice is one item of a dropdown.
type Choice struct {
	ID     string
	Label  string
	Detail string // muted, after the label
}

// Field draws a dropdown's closed field at r showing label (or the
// placeholder, muted, when label is empty) with a chevron; a click runs
// open.
func (h *Hits) Field(c *ui.Ctx, l *draw.List, r draw.Rect, label, detail, placeholder string, enabled bool, open func()) {
	pal := c.Pal
	hover := h.add(r, enabled, open)
	fill := pal.Surface
	if hover {
		fill = pal.SurfaceAlt
	}
	l.BorderedRect(r, fill, pal.Border, 1, draw.Uniform(6))
	col := pal.Text
	if label == "" {
		label, col = placeholder, pal.TextMuted
	}
	if !enabled {
		col = pal.TextFaint
	}
	lay := h.texts.Lay(c, label, text.SansRegular, labelSize)
	room := r.W - 2*12 - 18
	ty := r.Y + (r.H-lay.Height)/2
	h.texts.PutFit(c, l, label, text.SansRegular, labelSize, r.X+12, ty, room, col)
	if detail != "" && lay.Width+16 < room {
		dcol := pal.TextMuted
		if !enabled {
			dcol = pal.TextFaint
		}
		h.texts.PutFit(c, l, detail, text.SansRegular, 12, r.X+12+lay.Width+10, ty+1, room-lay.Width-10, dcol)
	}
	chevron(l, r.Right()-18, r.Y+r.H/2, pal.TextMuted)
}

// chevron draws a small down-pointing chevron centered at x, y.
func chevron(l *draw.List, x, y float32, col draw.Color) {
	l.Path(draw.Path{
		Points: []draw.Point{{X: x - 4, Y: y - 2}, {X: x, Y: y + 2}, {X: x + 4, Y: y - 2}},
		Stroke: col, Width: 1.5,
	})
}

// menuRow is the height of a dropdown item.
const menuRow = 30

// Menu draws an open dropdown's list under the field at field (above it
// when there's no room below in a window h tall), the current item
// checked, and records the items: a click picks one. Everything else in
// the window closes the menu (close): call Menu after drawing the rest.
func (h *Hits) Menu(c *ui.Ctx, l *draw.List, field draw.Rect, winH float32, choices []Choice, cur string, pick func(id string), close func()) {
	pal := c.Pal
	h.add(draw.Rect{X: -1e5, Y: -1e5, W: 2e5, H: 2e5}, true, close)
	n := len(choices)
	ht := float32(n)*menuRow + 8
	r := draw.Rect{X: field.X, Y: field.Bottom() + 4, W: field.W, H: ht}
	if r.Bottom() > winH-8 && field.Y-4-ht > 8 {
		r.Y = field.Y - 4 - ht
	}
	l.Shadow(r, pal.Shadow, draw.Uniform(8), 18)
	l.BorderedRect(r, pal.Surface, pal.Border, 1, draw.Uniform(8))
	y := r.Y + 4
	for _, ch := range choices {
		row := draw.Rect{X: r.X + 4, Y: y, W: r.W - 8, H: menuRow}
		id := ch.ID
		if h.add(row, true, func() { pick(id) }) {
			l.Rect(row, pal.SurfaceAlt, draw.Uniform(5))
		}
		lay := h.texts.Lay(c, ch.Label, text.SansRegular, labelSize)
		ty := row.Y + (row.H-lay.Height)/2
		if ch.ID == cur {
			check(l, row.X+12, row.Y+row.H/2, pal.Accent)
		}
		h.texts.PutFit(c, l, ch.Label, text.SansRegular, labelSize, row.X+28, ty, row.W-40, pal.Text)
		if ch.Detail != "" && lay.Width+40 < row.W-40 {
			h.texts.PutFit(c, l, ch.Detail, text.SansRegular, 12, row.X+28+lay.Width+10, ty+1, row.W-lay.Width-78, pal.TextMuted)
		}
		y += menuRow
	}
}

// check draws a small check mark centered at x, y.
func check(l *draw.List, x, y float32, col draw.Color) {
	l.Path(draw.Path{
		Points: []draw.Point{{X: x - 4, Y: y}, {X: x - 1, Y: y + 3}, {X: x + 5, Y: y - 4}},
		Stroke: col, Width: 1.75,
	})
}
