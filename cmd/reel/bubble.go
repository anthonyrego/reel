package main

// The camera bubble: a round window with the camera in it, mirrored,
// floating above every app on every Space. Display and area recordings
// show it as it is on screen (it's the one window of reel's they show), so
// where it sits is where it's recorded. Drag it to move it; scroll or
// pinch over it to change its size. Its look is changed on it too: under
// the pointer (only while no recording shows it) a handle at its
// bottom-right corner sizes it and a dot at its top-right rounds it, from
// a square to a circle; ⌥-drag moves the picture in it and ⌥-scroll (or
// ⌥-pinch) zooms. Nothing else is drawn on it, and nothing at all while
// it's recorded.

import (
	"image"
	"log/slog"
	"math"
	"time"

	"github.com/Zyko0/go-sdl3/sdl"

	"github.com/anthonyrego/reel/internal/draw"
	"github.com/anthonyrego/reel/internal/gfx"
	"github.com/anthonyrego/reel/internal/mac"
	"github.com/anthonyrego/reel/internal/platform"
	"github.com/anthonyrego/reel/internal/settings"
	"github.com/anthonyrego/reel/internal/text"
	"github.com/anthonyrego/reel/internal/theme"
	"github.com/anthonyrego/reel/internal/thumb"
	"github.com/anthonyrego/reel/internal/ui"
	"github.com/anthonyrego/reel/internal/ui/controls"
	"github.com/anthonyrego/reel/internal/ui/icons"
)

const (
	// The bubble's side, in points.
	bubbleMin, bubbleMax, bubbleDefault = 120, 420, 220
	// bubblePad is the room around the bubble for its shadow (and the
	// handles on a square's corners).
	bubblePad = 14
	// bubbleInset is how far from a display's (or an area's) edges the
	// bubble is placed.
	bubbleInset = 24
	// bubbleStalled is how often an occluded bubble (no drawable to draw
	// into) tries to draw a frame.
	bubbleStalled = 250 * time.Millisecond
	// handleHit is how near a handle a press takes it, in points.
	handleHit = 10
)

// bubbleLook is how the bubble frames the camera and how round it is.
type bubbleLook struct {
	crop  thumb.Crop // the part of the camera's view it shows
	round float32    // 0 a square, 1 a circle
}

// defaultLook is a new bubble's: a circle showing the middle square.
var defaultLook = bubbleLook{crop: thumb.Crop{Zoom: 1}, round: 1}

// lookOf is the look kept in the settings (the default if none).
func lookOf(b *settings.Bubble) bubbleLook {
	if b == nil {
		return defaultLook
	}
	return bubbleLook{crop: thumb.Crop{Zoom: b.Zoom, X: b.PanX, Y: b.PanY}, round: min(max(float32(b.Round), 0), 1)}
}

// bubblePart is a part of the bubble a press can take.
type bubblePart int

const (
	partNone    bubblePart = iota
	partShape              // the bubble: dragged, it moves
	partSize               // the bottom-right handle: dragged, it sizes the bubble
	partRound              // the top-right dot: dragged, it rounds the corners
	partPicture            // the picture, ⌥-dragged: it moves in the bubble
)

type bubble struct {
	a    *winApp
	feed *camFeed
	s    *surface
	// r is the bubble's square, in global points.
	r    draw.Rect
	look bubbleLook
	// keep, when set, is the rect the bubble must stay in and whether a
	// recording shows it now (the recorded display or area); otherwise
	// it stays in the display it's on.
	keep func() (draw.Rect, bool)
	// moved hears the bubble moved, resized or its look changed, at the
	// end of a drag or when the pointer leaves after a scroll (to keep
	// it).
	moved func(r draw.Rect, k bubbleLook)

	tex     *sdl.GPUTexture
	texSide int
	pic     bool // a picture uploaded since the camera was wanted
	tried   time.Time
	texts   controls.Texts

	// Under the pointer while no recording shows the bubble: over it (the
	// handles show), the part under it and whether ⌥ is held (the help
	// says how to frame instead).
	over    bool
	part    bubblePart
	framing bool

	drag         bubblePart // what the press drags (partNone: none)
	fromX, fromY float32    // the pointer at the press, global points
	from         draw.Rect
	fromLook     bubbleLook
	changed      bool
}

func newBubble(a *winApp, feed *camFeed) *bubble {
	b := &bubble{a: a, feed: feed}
	b.setLook(defaultLook)
	return b
}

// shown reports whether the bubble is on screen.
func (b *bubble) shown() bool { return b.s != nil && !b.s.hidden }

// show puts the bubble on screen at r (its square, global points).
func (b *bubble) show(r draw.Rect) {
	if b.s == nil {
		side := int32(r.W + 2*bubblePad)
		s, err := b.a.addWindow(gfx.WindowConfig{Title: "Reel camera", Width: int(side), Height: int(side), Hidden: true, Overlay: true}, b)
		if err != nil {
			slog.Error("bubble: open", "err", err)
			return
		}
		s.transparent, s.hidden = true, true
		// ⌘W on it hides it; the camera chip brings it back.
		s.onClose = b.hide
		b.s = s
	}
	b.place(r)
	if !b.s.hidden {
		return
	}
	w := b.s.win.Handle()
	if err := platform.Float(w, platform.Floating{SeeThrough: true, Level: platform.LevelFloating}); err != nil {
		slog.Warn("bubble: float", "err", err)
	}
	b.pic = false
	showWindow(b.s.win, false)
	b.s.hidden, b.s.dirty = false, true
}

// hide takes the bubble off screen (the window stays, so a recording that
// shows it keeps its number).
func (b *bubble) hide() {
	if b.s == nil || b.s.hidden {
		return
	}
	b.save()
	b.s.win.Handle().Hide()
	b.s.hidden = true
	b.pic = false
	b.over, b.part, b.framing = false, partNone, false
}

// place moves and sizes the bubble's window for the square r.
func (b *bubble) place(r draw.Rect) {
	b.r = r
	if b.s == nil {
		return
	}
	w := b.s.win.Handle()
	side := int32(math.Round(float64(r.W))) + 2*bubblePad
	w.SetSize(side, side)
	w.SetPosition(int32(math.Round(float64(r.X)))-bubblePad, int32(math.Round(float64(r.Y)))-bubblePad)
	b.feed.size.Store(int32(math.Round(float64(r.W * b.s.win.Scale()))))
	b.s.dirty = true
}

// setLook frames the camera and rounds the bubble as k says.
func (b *bubble) setLook(k bubbleLook) {
	b.look = k
	c := k.crop
	b.feed.crop.Store(&c)
	if b.s != nil {
		b.s.dirty = true
	}
}

// refresh is the refresh interval of the bubble's display.
func (b *bubble) refresh() time.Duration {
	return time.Duration(displayRefresh(b.s.win.Handle()) * float64(time.Second))
}

// windowNumber is the bubble's window as ScreenCaptureKit knows it.
func (b *bubble) windowNumber() (uint32, error) { return platform.WindowNumber(b.s.win.Handle()) }

// save tells the controller where the bubble is and how it looks, if
// that changed.
func (b *bubble) save() {
	if b.changed && b.moved != nil {
		b.moved(b.r, b.look)
	}
	b.changed = false
}

// bounds is what the bubble must stay in: the recorded rect, or the
// usable part of the display under its middle.
func (b *bubble) bounds() draw.Rect {
	if b.keep != nil {
		if r, ok := b.keep(); ok {
			return r
		}
	}
	return usableAt(b.r.X+b.r.W/2, b.r.Y+b.r.H/2)
}

// editing reports whether the bubble shows its handles and help under the
// pointer: unless a recording shows it.
func (b *bubble) editing() bool {
	if b.keep != nil {
		if _, rec := b.keep(); rec {
			return false
		}
	}
	return true
}

// shape is the bubble's square in its window; radius its corners'.
func (b *bubble) shape() draw.Rect { return draw.Rect{X: bubblePad, Y: bubblePad, W: b.r.W, H: b.r.H} }
func (b *bubble) radius() float32  { return b.look.round * b.r.W / 2 }

// usableAt is the usable part (below the menu bar, beside the Dock) of the
// display under a global point.
func usableAt(x, y float32) draw.Rect {
	id := sdl.GetDisplayForPoint(&sdl.Point{X: int32(x), Y: int32(y)})
	if id == 0 {
		id = sdl.GetPrimaryDisplay()
	}
	u, err := id.UsableBounds()
	if err != nil {
		return draw.Rect{W: 1440, H: 900}
	}
	return draw.Rect{X: float32(u.X), Y: float32(u.Y), W: float32(u.W), H: float32(u.H)}
}

// frame takes the camera's newest frame into the bubble's texture and
// looks where the pointer is: the loop redraws the bubble when it says
// so. Main thread.
func (b *bubble) frame() bool {
	if b.s == nil {
		return false
	}
	got := b.feed.take(func(img *image.RGBA) {
		side := img.Rect.Dx()
		var err error
		if b.tex == nil || b.texSide != side {
			if b.tex != nil {
				b.a.renderer.SetImage(cameraImage, nil)
				b.a.device.ReleaseTexture(b.tex)
				b.tex = nil
			}
			b.tex, err = gfx.UploadTexture(b.a.device, sdl.GPU_TEXTUREFORMAT_R8G8B8A8_UNORM, uint32(side), uint32(side), img.Pix)
			b.texSide = side
		} else {
			err = gfx.UpdateTexture(b.a.device, b.tex, 0, 0, uint32(side), uint32(side), img.Pix)
		}
		if err != nil {
			slog.Error("bubble: upload a frame", "err", err)
			return
		}
		b.a.renderer.SetImage(cameraImage, b.tex)
		b.pic = true
	})
	hover := b.poll()
	if !(got || hover) || b.s.hidden {
		return false
	}
	// Covered or on a Space not shown: acquiring a drawable can wait
	// about a second, so don't try every frame.
	if b.s.stalled && time.Since(b.tried) < bubbleStalled {
		return false
	}
	b.tried = time.Now()
	return true
}

// poll looks where the pointer is and whether ⌥ is held, and reports
// whether the bubble should draw again for it. The bubble is seldom the
// key window, which may not hear the pointer move, so it asks, at every
// camera frame and every event.
func (b *bubble) poll() bool {
	over, part, framing := false, partNone, false
	if b.s != nil && !b.s.hidden && b.editing() {
		_, gx, gy := sdl.GetGlobalMouseState()
		p := draw.Point{X: gx - b.r.X + bubblePad, Y: gy - b.r.Y + bubblePad}
		side := b.r.W + 2*bubblePad
		over = b.drag != partNone || (p.X >= 0 && p.Y >= 0 && p.X < side && p.Y < side)
		if over {
			framing = mac.OptionHeld()
			part = partAt(p, b.shape(), b.radius(), !framing)
		}
		if b.drag != partNone {
			part = b.drag
		}
	}
	if over == b.over && part == b.part && framing == b.framing {
		return false
	}
	if part != b.part {
		setCursor(cursorFor(part))
	}
	b.over, b.part, b.framing = over, part, framing
	return true
}

// cursorFor is the cursor over a part of the bubble.
func cursorFor(p bubblePart) ui.Cursor {
	switch p {
	case partSize:
		return ui.CursorResizeDiag
	case partRound:
		return ui.CursorPointer
	}
	return ui.CursorDefault
}

// close lets the texture go (at quit).
func (b *bubble) close() {
	if b.tex != nil {
		b.a.renderer.SetImage(cameraImage, nil)
		b.a.device.ReleaseTexture(b.tex)
		b.tex = nil
	}
}

func (b *bubble) draw(c *ui.Ctx, l *draw.List, w, h float32) {
	st, _ := b.feed.status()
	f := bubbleFace{pic: b.pic && st != camFailed, failed: st == camFailed, round: b.look.round}
	if b.over && b.editing() {
		f.handles, f.framing, f.hint, f.hot = !b.framing, b.framing, b.drag == partNone, b.part
	}
	drawBubble(c, l, &b.texts, w, h, f)
}

// bubbleFace is what the bubble draws.
type bubbleFace struct {
	// pic: the camera's picture (else a camera while it starts, or one
	// struck through when it failed).
	pic, failed bool
	round       float32
	// handles: the size and roundness handles (hot: the one under the
	// pointer, or dragged); hint: a line of help at the bottom, how to
	// frame (framing: what ⌥ does, held now).
	handles, framing, hint bool
	hot                    bubblePart
}

// drawBubble draws the bubble into a w×h window: its square in the middle,
// rounded as f says, with its shadow around it.
func drawBubble(c *ui.Ctx, l *draw.List, t *controls.Texts, w, h float32, f bubbleFace) {
	pal := c.Pal
	d := min(w, h) - 2*bubblePad
	shape := draw.Rect{X: (w - d) / 2, Y: (h - d) / 2, W: d, H: d}
	rad := f.round * d / 2
	round := draw.Uniform(rad)
	l.Shadow(shape, pal.Shadow, round, bubblePad-2)
	if f.pic {
		l.Image(shape, cameraImage, round, 255)
	} else {
		l.Rect(shape, pal.Bar, round)
		icon := icons.Camera
		if f.failed {
			icon = icons.CameraOff
		}
		icon(l, w/2, h/2, max(24, d*0.2), pal.TextMuted)
	}
	l.BorderedRect(shape, draw.Color{}, pal.Text.WithAlpha(0x2e), 1.5, round)
	if f.hint {
		lines := []string{"Hold ⌥ to frame", "⌥ to frame"}
		if f.framing {
			lines = []string{"Drag to frame · scroll to zoom", "Drag or scroll to frame", "Drag to frame"}
		}
		bubbleHint(c, l, t, shape, rad, lines)
	}
	if f.handles {
		rh, sh := handlePoints(shape, rad)
		bubbleHandle(l, pal, sh, 6, false, f.hot == partSize)
		bubbleHandle(l, pal, rh, 5, true, f.hot == partRound)
	}
}

// bubbleHandle draws one of the bubble's handles at p: a light dot ringed
// dark (hollow: a light ring), larger when hot.
func bubbleHandle(l *draw.List, pal theme.Palette, p draw.Point, r float32, hollow, hot bool) {
	if hot {
		r += 1.5
	}
	dot := draw.Rect{X: p.X - r, Y: p.Y - r, W: 2 * r, H: 2 * r}
	l.Shadow(dot, pal.Shadow, draw.Uniform(r), 4)
	if hollow {
		l.BorderedRect(dot, pal.Bar.WithAlpha(0x99), pal.Text, 2, draw.Uniform(r))
		return
	}
	l.BorderedRect(dot, pal.Text, pal.Bar, 1.5, draw.Uniform(r))
}

// bubbleHint draws a line of help in a pill at the bottom inside the
// shape, low and off the face: as low as the shape is wide enough for the
// first of lines that fits, up to a quarter of the way (past that, the
// last line, cut to fit).
func bubbleHint(c *ui.Ctx, l *draw.List, t *controls.Texts, shape draw.Rect, rad float32, lines []string) {
	const size, padX, pillH, margin = 11, 9, 20, 8
	pal := c.Pal
	put := func(s string, k float32) {
		lay := t.Lay(c, s, text.SansSemibold, size)
		w := min(lay.Width+2*padX, widthAt(shape, rad, k)-2*margin)
		pill := draw.Rect{X: shape.X + (shape.W-w)/2, Y: shape.Bottom() - k - pillH, W: w, H: pillH}
		l.Rect(pill, pal.Bar.WithAlpha(0xd8), draw.Uniform(pillH/2))
		t.PutFit(c, l, s, text.SansSemibold, size, pill.X+padX, pill.Y+(pillH-lay.Height)/2, w-2*padX, pal.Text)
	}
	// k is the pill's bottom, above the shape's.
	low, high := max(8, shape.H*0.08), shape.H/4
	for _, s := range lines {
		want := t.Lay(c, s, text.SansSemibold, size).Width + 2*padX + 2*margin
		for k := low; k <= high; k += 2 {
			if want <= widthAt(shape, rad, k) {
				put(s, k)
				return
			}
		}
	}
	put(lines[len(lines)-1], high)
}

// widthAt is how wide a square with corners of radius rad is k above its
// bottom.
func widthAt(shape draw.Rect, rad, k float32) float32 {
	if k >= rad {
		return shape.W
	}
	in := rad - float32(math.Sqrt(float64(rad*rad-(rad-k)*(rad-k))))
	return shape.W - 2*in
}

// handlePoints are where the outline of a square with corners of radius
// rad crosses the diagonals at its top-right and bottom-right corners: the
// roundness dot's place and the size handle's. On a square, the corners.
func handlePoints(shape draw.Rect, rad float32) (round, size draw.Point) {
	in := rad * (1 - 1/math.Sqrt2)
	return draw.Point{X: shape.Right() - in, Y: shape.Y + in}, draw.Point{X: shape.Right() - in, Y: shape.Bottom() - in}
}

// partAt is the part of a bubble whose square is shape (corners of radius
// rad) at p: a handle (if they're shown), the bubble, or none (outside
// it: the corners of its window aren't it).
func partAt(p draw.Point, shape draw.Rect, rad float32, handles bool) bubblePart {
	if handles {
		rh, sh := handlePoints(shape, rad)
		near := func(q draw.Point) bool { return (p.X-q.X)*(p.X-q.X)+(p.Y-q.Y)*(p.Y-q.Y) <= handleHit*handleHit }
		switch {
		case near(sh):
			return partSize
		case near(rh):
			return partRound
		}
	}
	if inShape(p, shape, rad) {
		return partShape
	}
	return partNone
}

// inShape reports whether p is in a square with corners of radius rad.
func inShape(p draw.Point, shape draw.Rect, rad float32) bool {
	if p.X < shape.X || p.Y < shape.Y || p.X > shape.Right() || p.Y > shape.Bottom() {
		return false
	}
	qx := max(shape.X+rad-p.X, p.X-(shape.Right()-rad), 0)
	qy := max(shape.Y+rad-p.Y, p.Y-(shape.Bottom()-rad), 0)
	return qx*qx+qy*qy <= rad*rad
}

func (b *bubble) event(c *ui.Ctx, ev ui.Event) bool {
	if b.s == nil {
		return false
	}
	option := ev.Mods&ui.Alt != 0 || mac.OptionHeld()
	redraw := false
	switch ev.Kind {
	case ui.PointerDown:
		if ev.Button != 1 {
			break
		}
		part := partAt(ev.Pos, b.shape(), b.radius(), b.editing() && !option)
		if part == partShape && option {
			part = partPicture
		}
		if part == partNone {
			break
		}
		_, gx, gy := sdl.GetGlobalMouseState()
		b.drag, b.fromX, b.fromY, b.from, b.fromLook = part, gx, gy, b.r, b.look
		redraw = true // the help goes while dragging
	case ui.PointerMove:
		if b.drag == partNone {
			break
		}
		_, gx, gy := sdl.GetGlobalMouseState()
		dx, dy := gx-b.fromX, gy-b.fromY
		k := b.look
		switch b.drag {
		case partShape:
			r := b.from
			r.X, r.Y = r.X+dx, r.Y+dy
			b.place(clampBubble(r, b.bounds(), bubbleMin, bubbleMax))
		case partSize:
			b.place(resizeFromCorner(b.from, dx, dy, b.bounds()))
		case partRound:
			k.round = roundBy(b.fromLook.round, b.r.W, dx, dy)
			b.setLook(k)
		case partPicture:
			w, h := b.feed.frameSize()
			k.crop = panBy(b.fromLook.crop, dx, dy, b.r.W, w, h)
			b.setLook(k)
		}
		b.changed = true
	case ui.PointerUp:
		if b.drag != partNone {
			b.drag = partNone
			b.save()
			redraw = true
		}
	case ui.PointerLeave:
		if b.drag == partNone {
			b.save()
		}
	case ui.Wheel:
		// Scrolling up (content moving down) makes it larger, or zooms in.
		by := float32(math.Exp(float64(-ev.DY) / 300))
		if option {
			b.zoom(by)
		} else {
			b.resize(by)
		}
	case ui.Pinch:
		if option {
			b.zoom(ev.Scale)
		} else {
			b.resize(ev.Scale)
		}
	}
	return b.poll() || redraw
}

// resize scales the bubble about its middle.
func (b *bubble) resize(by float32) {
	if by <= 0 || b.drag != partNone {
		return
	}
	r := b.r
	side := r.W * by
	r = draw.Rect{X: r.X + r.W/2 - side/2, Y: r.Y + r.H/2 - side/2, W: side, H: side}
	b.place(clampBubble(r, b.bounds(), bubbleMin, bubbleMax))
	b.changed = true
}

// zoom zooms the picture in the bubble about its middle.
func (b *bubble) zoom(by float32) {
	if by <= 0 || b.drag != partNone {
		return
	}
	w, h := b.feed.frameSize()
	k := b.look
	k.crop.Zoom = max(k.crop.Zoom, 1) * float64(by)
	k.crop = k.crop.Clamp(w, h)
	b.setLook(k)
	b.changed = true
}

func (b *bubble) animating() bool { return false }
func (b *bubble) woke()           {}

// roundBy is how round a bubble d points wide is once its dot, at from,
// is dragged by dx, dy: the dot follows the pointer along the top-right
// corner's diagonal, inward rounder, on the outline throughout.
func roundBy(from, d, dx, dy float32) float32 {
	along := (dy - dx) / math.Sqrt2
	return min(max(from+along/(d/2*(math.Sqrt2-1)), 0), 1)
}

// resizeFromCorner is from sized by its bottom-right handle dragged by dx,
// dy: the top-left corner stays, the side between the bubble's least and
// most and no more than bounds leave right and below.
func resizeFromCorner(from draw.Rect, dx, dy float32, bounds draw.Rect) draw.Rect {
	hi := min(bubbleMax, bounds.Right()-from.X, bounds.Bottom()-from.Y)
	side := min(max(from.W+(dx+dy)/2, min(bubbleMin, hi)), hi)
	return draw.Rect{X: from.X, Y: from.Y, W: side, H: side}
}

// panBy is c with the picture dragged by dx, dy in a bubble d points
// wide over w×h frames: what was under the pointer stays under it.
func panBy(c thumb.Crop, dx, dy, d float32, w, h int) thumb.Crop {
	c = c.Clamp(w, h)
	s := float64(min(w, h)) / c.Zoom
	c.X -= float64(dx/d) * s / float64(w)
	c.Y -= float64(dy/d) * s / float64(h)
	return c.Clamp(w, h)
}

// clampBubble keeps a bubble (its square) inside bounds: its side between
// lo and hi but no bigger than bounds, the same middle, then moved in.
func clampBubble(r, bounds draw.Rect, lo, hi float32) draw.Rect {
	side := min(max(r.W, lo), hi, bounds.W, bounds.H)
	cx, cy := r.X+r.W/2, r.Y+r.H/2
	r = draw.Rect{X: cx - side/2, Y: cy - side/2, W: side, H: side}
	r.X = min(max(r.X, bounds.X), bounds.Right()-side)
	r.Y = min(max(r.Y, bounds.Y), bounds.Bottom()-side)
	return r
}

// fitInto is where the bubble goes so a recording of target shows it:
// where it is, if it's inside; otherwise in target's bottom-left corner,
// as large as it was if that fits.
func fitInto(r, target draw.Rect) draw.Rect {
	if r.X >= target.X && r.Y >= target.Y && r.Right() <= target.Right() && r.Bottom() <= target.Bottom() {
		return r
	}
	inset := float32(bubbleInset)
	room := min(target.W, target.H) - 2*inset
	if room < bubbleMin/2 { // a small area: the bubble fills most of it
		inset = min(target.W, target.H) * 0.1
		room = min(target.W, target.H) - 2*inset
	}
	side := max(min(r.W, room), min(bubbleMin, room))
	return draw.Rect{X: target.X + inset, Y: target.Bottom() - inset - side, W: side, H: side}
}
