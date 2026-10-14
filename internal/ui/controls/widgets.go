package controls

import (
	"github.com/anthonyrego/reel/internal/draw"
	"github.com/anthonyrego/reel/internal/text"
	"github.com/anthonyrego/reel/internal/theme"
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
	buttonR   = 7
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
		l.BorderedRect(r, pal.Surface, pal.Border, 1, draw.Uniform(buttonR))
		Put(l, lay, tx, ty, pal.TextFaint)
	case s == Primary || s == Record:
		fill := pal.Accent
		if s == Record {
			fill = pal.Live
		}
		if hover {
			fill = fill.WithAlpha(0xd8)
		}
		l.Rect(r, fill, draw.Uniform(buttonR))
		on := OnFill(pal)
		if s == Record {
			l.Rect(draw.Rect{X: tx, Y: r.Y + r.H/2 - 5, W: 10, H: 10}, on, draw.Uniform(5))
			tx += 16
		}
		Put(l, lay, tx, ty, on)
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
		l.BorderedRect(r, fill, pal.Border, 1, draw.Uniform(buttonR))
		Put(l, lay, tx, ty, col)
	}
	return r
}

// OnFill is the color of a label on a filled (accent or red) control.
func OnFill(pal theme.Palette) draw.Color {
	if pal.Dark {
		return pal.Text
	}
	return pal.Surface
}

// Choice is one item of a dropdown.
type Choice struct {
	ID     string
	Label  string
	Detail string // muted, after the label
}

// Menu layout.
const (
	menuRow  = 30
	menuMaxW = 420
)

// Menu draws an open dropdown's list under the control at field (above it
// when there's more room there) in a window w×h, the current item
// checked, and records the items: a click picks one. It's as wide as its
// widest item (at least the field, at most menuMaxW) and as tall as fits;
// the wheel scrolls the rest. Everything else in the window closes the
// menu (close): call Menu after drawing the rest.
func (h *Hits) Menu(c *ui.Ctx, l *draw.List, field draw.Rect, w, ht float32, choices []Choice, cur string, pick func(id string), close func()) {
	pal := c.Pal
	h.add(draw.Rect{X: -1e5, Y: -1e5, W: 2e5, H: 2e5}, true, close)
	n := len(choices)
	width := field.W
	for _, ch := range choices {
		cw := 28 + h.texts.Lay(c, ch.Label, text.SansRegular, labelSize).Width + 16
		if ch.Detail != "" {
			cw += 10 + h.texts.Lay(c, ch.Detail, text.SansRegular, 12).Width
		}
		width = max(width, min(cw+8, menuMaxW))
	}
	width = min(width, w-16)
	below, above := ht-8-(field.Bottom()+4), field.Y-4-8
	rows := min(n, int((max(below, above)-8)/menuRow))
	rows = max(rows, min(n, 1))
	r := draw.Rect{X: max(8, min(field.X, w-8-width)), Y: field.Bottom() + 4, W: width, H: float32(rows)*menuRow + 8}
	if r.Bottom() > ht-8 && above > below {
		r.Y = field.Y - 4 - r.H
	}
	h.menuRect, h.menuMax, h.menuOpen = r, float32(n-rows)*menuRow, true
	if !h.menuWas {
		// Just opened: the current item in view.
		h.menuScroll = 0
		for i, ch := range choices {
			if ch.ID == cur && i >= rows {
				h.menuScroll = float32(i-rows+1) * menuRow
			}
		}
	}
	h.menuScroll = max(0, min(h.menuScroll, h.menuMax))
	l.Shadow(r, pal.Shadow, draw.Uniform(10), 18)
	l.BorderedRect(r, pal.Surface.WithAlpha(0xff), pal.Border, 1, draw.Uniform(10))
	inner := draw.Rect{X: r.X, Y: r.Y + 4, W: r.W, H: r.H - 8}
	l.PushClip(inner)
	defer l.PopClip()
	y := inner.Y - h.menuScroll
	for _, ch := range choices {
		row := draw.Rect{X: r.X + 4, Y: y, W: r.W - 8, H: menuRow}
		y += menuRow
		if row.Bottom() <= inner.Y || row.Y >= inner.Bottom() {
			continue
		}
		id := ch.ID
		if h.add(row.Intersect(inner), true, func() { pick(id) }) {
			l.Rect(row, pal.SurfaceAlt, draw.Uniform(6))
		}
		lay := h.texts.Lay(c, ch.Label, text.SansRegular, labelSize)
		ty := row.Y + (row.H-lay.Height)/2
		if ch.ID == cur {
			check(l, row.X+12, row.Y+row.H/2, pal.Accent)
		}
		h.texts.PutFit(c, l, ch.Label, text.SansRegular, labelSize, row.X+28, ty, row.W-40, pal.Text)
		if ch.Detail != "" && lay.Width+40 < row.W-40 {
			h.texts.PutFit(c, l, ch.Detail, text.SansRegular, 12, row.X+28+lay.Width+10, ty+1, row.W-lay.Width-50, pal.TextMuted)
		}
	}
}

// check draws a small check mark centered at x, y.
func check(l *draw.List, x, y float32, col draw.Color) {
	l.Path(draw.Path{
		Points: []draw.Point{{X: x - 4, Y: y}, {X: x - 1, Y: y + 3}, {X: x + 5, Y: y - 4}},
		Stroke: col, Width: 1.75,
	})
}
