// Package controls draws reel's small controls (buttons, a switch, a
// segmented control, a dropdown) into a draw.List and remembers where each
// was drawn, so a click finds it. A view draws its controls every frame
// through a Hits, then hands pointer events to the same Hits. Pure.
//
// The control drawing follows an earlier project of the author's
// (its settings panel), generalized out of it.
package controls

import (
	"github.com/anthonyrego/reel/internal/draw"
	"github.com/anthonyrego/reel/internal/text"
	"github.com/anthonyrego/reel/internal/ui"
)

// hit is one clickable rect drawn this frame.
type hit struct {
	r       draw.Rect
	do      func()
	enabled bool
}

// Hits records this frame's controls and routes the pointer to them.
type Hits struct {
	hits  []hit
	hover int // index into hits; -1 for none
	press int // where the button went down; -1 for none
	texts Texts
}

// NewHits returns an empty registry.
func NewHits() *Hits { return &Hits{hover: -1, press: -1} }

// Begin starts a frame: the controls drawn after it are the ones that
// take clicks.
func (h *Hits) Begin() { h.hits = h.hits[:0] }

// add records a control and reports whether the pointer is over it.
func (h *Hits) add(r draw.Rect, enabled bool, do func()) (hovered bool) {
	h.hits = append(h.hits, hit{r: r, do: do, enabled: enabled})
	return enabled && h.hover == len(h.hits)-1
}

// Area records a clickable area drawn some other way (a list row) and
// reports whether the pointer is over it.
func (h *Hits) Area(r draw.Rect, do func()) bool { return h.add(r, true, do) }

// Event handles a pointer event: hover follows the pointer, and a click
// (press and release on the same enabled control) runs it. It reports
// whether to redraw, whether the event was on a control, and whether the
// pointer is over one (for the cursor).
func (h *Hits) Event(ev ui.Event) (redraw, handled, hand bool) {
	at := -1
	for i := len(h.hits) - 1; i >= 0; i-- { // the last drawn is on top
		if h.hits[i].r.Contains(ev.Pos) {
			at = i
			break
		}
	}
	switch ev.Kind {
	case ui.PointerMove:
		if at != h.hover {
			h.hover = at
			redraw = true
		}
	case ui.PointerLeave:
		redraw = h.hover >= 0
		h.hover, h.press = -1, -1
	case ui.PointerDown:
		if ev.Button == 1 {
			h.press = at
			handled = at >= 0
		}
	case ui.PointerUp:
		if ev.Button == 1 && at >= 0 && at == h.press && h.hits[at].enabled && h.hits[at].do != nil {
			h.press = -1
			h.hits[at].do()
			return true, true, true
		}
		h.press = -1
	}
	hand = at >= 0 && h.hits[at].enabled
	return redraw, handled || at >= 0, hand
}

// Texts shapes control labels once per string, font and size.
func (h *Hits) Texts() *Texts { return &h.texts }

// Texts caches shaped one-line labels (recolored at draw time).
type Texts struct{ m map[textKey]text.Layout }

type textKey struct {
	s    string
	font draw.FontID
	size float32
}

// Lay shapes s.
func (t *Texts) Lay(c *ui.Ctx, s string, font draw.FontID, size float32) text.Layout {
	if t.m == nil || len(t.m) > 1024 {
		t.m = map[textKey]text.Layout{}
	}
	k := textKey{s, font, size}
	if l, ok := t.m[k]; ok {
		return l
	}
	l := c.Text.Layout([]text.Run{{Text: s, Style: text.Style{Font: font, Size: size, Color: c.Pal.Text}}}, 0)
	t.m[k] = l
	return l
}

// Put draws a shaped label recolored with its top-left corner at x, y.
func Put(l *draw.List, lay text.Layout, x, y float32, col draw.Color) {
	for _, line := range lay.Lines {
		for _, run := range line.Runs {
			run.Origin.X += x
			run.Origin.Y += y
			run.Color = col
			l.Glyphs(run)
		}
	}
}

// PutFit draws a one-line label at x, y within width w: when it doesn't
// fit, the glyphs that would cross the edge are dropped and "…" ends it.
//
// Copied from an earlier project of the author's
// (putFitAt).
func (t *Texts) PutFit(c *ui.Ctx, l *draw.List, s string, font draw.FontID, size, x, y, w float32, col draw.Color) {
	lay := t.Lay(c, s, font, size)
	if lay.Width <= w || len(lay.Lines) == 0 {
		Put(l, lay, x, y, col)
		return
	}
	ell := t.Lay(c, "…", font, size)
	room := w - ell.Width
	line := lay.Lines[0]
	// Pen positions across the line's runs (fallback faces split a line
	// into several); a glyph's right edge is the next glyph's pen.
	var pens []float32
	for _, run := range line.Runs {
		for _, g := range run.Glyphs {
			pens = append(pens, run.Origin.X+g.X)
		}
	}
	keep, cut := 0, float32(0)
	for k := range pens {
		right := line.Width
		if k+1 < len(pens) {
			right = pens[k+1]
		}
		if right > room {
			break
		}
		keep, cut = k+1, right
	}
	for _, run := range line.Runs {
		if keep <= 0 {
			break
		}
		r := run
		r.Glyphs = run.Glyphs[:min(len(run.Glyphs), keep)]
		keep -= len(r.Glyphs)
		r.Origin.X += x
		r.Origin.Y += y
		r.Color = col
		l.Glyphs(r)
	}
	Put(l, ell, x+cut, y+(lay.Height-ell.Height)/2, col)
}

// Rects are the controls drawn this frame, in order (tests).
func (h *Hits) Rects() []draw.Rect {
	out := make([]draw.Rect, len(h.hits))
	for i, x := range h.hits {
		out[i] = x.r
	}
	return out
}
