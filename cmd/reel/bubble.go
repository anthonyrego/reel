package main

// The camera bubble: a round window with the camera in it, mirrored,
// floating above every app on every Space. Display and area recordings
// show it as it is on screen (it's the one window of reel's they show), so
// where it sits is where it's recorded. Drag it to move it; scroll or
// pinch over it to change its size. Nothing else is drawn on it, so
// nothing else ends up in a recording.

import (
	"image"
	"log/slog"
	"math"
	"time"

	"github.com/Zyko0/go-sdl3/sdl"

	"github.com/anthonyrego/reel/internal/draw"
	"github.com/anthonyrego/reel/internal/gfx"
	"github.com/anthonyrego/reel/internal/platform"
	"github.com/anthonyrego/reel/internal/ui"
	"github.com/anthonyrego/reel/internal/ui/icons"
)

const (
	// The circle's diameter, in points.
	bubbleMin, bubbleMax, bubbleDefault = 120, 420, 220
	// bubblePad is the room around the circle for its shadow.
	bubblePad = 14
	// bubbleInset is how far from a display's (or an area's) edges the
	// bubble is placed.
	bubbleInset = 24
	// bubbleStalled is how often an occluded bubble (no drawable to draw
	// into) tries to draw a frame.
	bubbleStalled = 250 * time.Millisecond
)

type bubble struct {
	a    *winApp
	feed *camFeed
	s    *surface
	// r is the circle, in global points.
	r draw.Rect
	// keep, when set, is the rect the bubble must stay in (the recorded
	// display or area while recording); otherwise the display it's on.
	keep func() (draw.Rect, bool)
	// moved hears the bubble moved or resized, at the end of a drag or
	// when the pointer leaves after a resize (to keep it).
	moved func(r draw.Rect)

	tex     *sdl.GPUTexture
	texSide int
	pic     bool // a picture uploaded since the camera was wanted
	tried   time.Time

	dragging     bool
	fromX, fromY float32 // the pointer, global points
	from         draw.Rect
	resized      bool
}

func newBubble(a *winApp, feed *camFeed) *bubble { return &bubble{a: a, feed: feed} }

// shown reports whether the bubble is on screen.
func (b *bubble) shown() bool { return b.s != nil && !b.s.hidden }

// show puts the bubble on screen at r (the circle, global points).
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
}

// place moves and sizes the bubble's window for the circle r.
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

// refresh is the refresh interval of the bubble's display.
func (b *bubble) refresh() time.Duration {
	return time.Duration(displayRefresh(b.s.win.Handle()) * float64(time.Second))
}

// windowNumber is the bubble's window as ScreenCaptureKit knows it.
func (b *bubble) windowNumber() (uint32, error) { return platform.WindowNumber(b.s.win.Handle()) }

// save tells the controller where the bubble is, if it moved.
func (b *bubble) save() {
	if b.resized && b.moved != nil {
		b.moved(b.r)
	}
	b.resized = false
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

// frame takes the camera's newest frame into the bubble's texture: the
// loop redraws the bubble when it says so. Main thread.
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
	if !got || b.s.hidden {
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
	drawBubble(c, l, w, h, b.pic && st != camFailed, st == camFailed)
}

// drawBubble draws the bubble into a w×h window: the circle in the
// middle with its shadow around it, showing the camera (pic), or a camera
// while it starts, or a camera struck through when it failed.
func drawBubble(c *ui.Ctx, l *draw.List, w, h float32, pic, failed bool) {
	pal := c.Pal
	d := min(w, h) - 2*bubblePad
	circle := draw.Rect{X: (w - d) / 2, Y: (h - d) / 2, W: d, H: d}
	round := draw.Uniform(d / 2)
	l.Shadow(circle, pal.Shadow, round, bubblePad-2)
	if pic {
		l.Image(circle, cameraImage, round, 255)
	} else {
		l.Rect(circle, pal.Bar, round)
		icon := icons.Camera
		if failed {
			icon = icons.CameraOff
		}
		icon(l, w/2, h/2, max(24, d*0.2), pal.TextMuted)
	}
	l.BorderedRect(circle, draw.Color{}, pal.Text.WithAlpha(0x2e), 1.5, round)
}

func (b *bubble) event(c *ui.Ctx, ev ui.Event) bool {
	if b.s == nil {
		return false
	}
	switch ev.Kind {
	case ui.PointerDown:
		// Only the circle: the corners of its square window aren't it.
		in := ev.Pos.X - bubblePad - b.r.W/2
		jn := ev.Pos.Y - bubblePad - b.r.H/2
		if ev.Button == 1 && in*in+jn*jn <= b.r.W*b.r.W/4 {
			_, gx, gy := sdl.GetGlobalMouseState()
			b.dragging, b.fromX, b.fromY, b.from = true, gx, gy, b.r
		}
	case ui.PointerMove:
		if b.dragging {
			_, gx, gy := sdl.GetGlobalMouseState()
			r := b.from
			r.X, r.Y = r.X+gx-b.fromX, r.Y+gy-b.fromY
			b.place(clampBubble(r, b.bounds(), bubbleMin, bubbleMax))
			b.resized = true
		}
	case ui.PointerUp:
		if b.dragging {
			b.dragging = false
			b.save()
		}
	case ui.PointerLeave:
		if !b.dragging {
			b.save()
		}
	case ui.Wheel:
		// Scrolling up (content moving down) makes it larger.
		b.resize(float32(math.Exp(float64(-ev.DY) / 300)))
	case ui.Pinch:
		b.resize(ev.Scale)
	}
	return false
}

// resize scales the bubble about its middle.
func (b *bubble) resize(by float32) {
	if by <= 0 || b.dragging {
		return
	}
	r := b.r
	side := r.W * by
	r = draw.Rect{X: r.X + r.W/2 - side/2, Y: r.Y + r.H/2 - side/2, W: side, H: side}
	b.place(clampBubble(r, b.bounds(), bubbleMin, bubbleMax))
	b.resized = true
}

func (b *bubble) animating() bool { return false }
func (b *bubble) woke()           {}

// clampBubble keeps a bubble (the square around its circle) inside
// bounds: its side between lo and hi but no bigger than bounds, the same
// middle, then moved in.
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
