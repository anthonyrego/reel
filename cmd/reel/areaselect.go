package main

// Selecting an area: a still of the display under the pointer (without
// reel's windows) fills the display above the menu bar, dimmed; the user
// drags out the part to record, which shows undimmed with its size. Esc
// cancels. The area is kept in the display's own points: what
// ScreenCaptureKit's sourceRect takes.

import (
	"fmt"
	"log/slog"
	"math"

	"github.com/Zyko0/go-sdl3/sdl"

	"github.com/anthonyrego/reel/internal/draw"
	"github.com/anthonyrego/reel/internal/gfx"
	"github.com/anthonyrego/reel/internal/mac"
	"github.com/anthonyrego/reel/internal/platform"
	"github.com/anthonyrego/reel/internal/text"
	"github.com/anthonyrego/reel/internal/ui"
	"github.com/anthonyrego/reel/internal/ui/controls"
)

// areaImage is the image id the display's still is drawn with.
const areaImage draw.ImageID = 1

// minArea is the smallest area a drag selects, in points.
const minArea = 40

// selectArea starts selecting on the display under the pointer.
func selectArea(a *winApp, c *controller) {
	_, gx, gy := sdl.GetGlobalMouseState()
	sid := sdl.GetDisplayForPoint(&sdl.Point{X: int32(gx), Y: int32(gy)})
	bounds, err := sid.Bounds()
	if err != nil {
		c.errText = "Couldn't find the display: " + err.Error()
		return
	}
	var display *mac.Display
	for i, d := range c.content.Displays {
		f := d.Frame
		if int32(f.X) == bounds.X && int32(f.Y) == bounds.Y && int32(f.W) == bounds.W && int32(f.H) == bounds.H {
			display = &c.content.Displays[i]
		}
	}
	if display == nil {
		c.errText = "Couldn't match the display to record (try again after reconnecting it)"
		c.refresh()
		return
	}
	d := *display
	go func() {
		img, err := mac.Screenshot(d.ID, 0)
		c.post(func() {
			if err != nil {
				c.errText = "Couldn't take a still of the display: " + err.Error()
				return
			}
			tex, err := gfx.UploadTexture(a.device, sdl.GPU_TEXTUREFORMAT_R8G8B8A8_UNORM, uint32(img.Bounds().Dx()), uint32(img.Bounds().Dy()), img.Pix)
			if err != nil {
				c.errText = "Couldn't show the display: " + err.Error()
				return
			}
			a.renderer.SetImage(areaImage, tex)
			sc := &areaScene{c: c, display: d, hits: controls.NewHits()}
			s, err := a.addWindow(gfx.WindowConfig{Title: "Reel area", Width: int(bounds.W), Height: int(bounds.H), Hidden: true, Overlay: true}, sc)
			if err != nil {
				a.renderer.SetImage(areaImage, nil)
				a.device.ReleaseTexture(tex)
				c.errText = "Couldn't open the area picker: " + err.Error()
				return
			}
			sc.close = func() {
				a.removeWindow(s)
				a.renderer.SetImage(areaImage, nil)
				a.device.ReleaseTexture(tex)
				setCursor(ui.CursorDefault)
				a.window.Handle().Raise()
			}
			s.onClose = sc.close
			w := s.win.Handle()
			if err := platform.Float(w, platform.Floating{Level: platform.LevelOverMenuBar}); err != nil {
				slog.Warn("area: float", "err", err)
			}
			w.SetPosition(bounds.X, bounds.Y)
			w.SetSize(bounds.W, bounds.H)
			w.Show()
			w.Raise()
			crosshair()
		})
	}()
}

// crosshair shows the crosshair cursor.
func crosshair() {
	if cur, err := sdl.CreateSystemCursor(sdl.SYSTEM_CURSOR_CROSSHAIR); err == nil {
		sdl.SetCursor(cur)
	}
}

type areaScene struct {
	c       *controller
	display mac.Display
	hits    *controls.Hits
	close   func()

	dragging bool
	from, to draw.Point
	have     bool
}

// rect is the selection, in the window's (the display's) points, whole.
func (s *areaScene) rect() draw.Rect {
	x0, x1 := min(s.from.X, s.to.X), max(s.from.X, s.to.X)
	y0, y1 := min(s.from.Y, s.to.Y), max(s.from.Y, s.to.Y)
	x0, y0 = float32(math.Floor(float64(x0))), float32(math.Floor(float64(y0)))
	x1, y1 = float32(math.Ceil(float64(x1))), float32(math.Ceil(float64(y1)))
	return draw.Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
}

func (s *areaScene) draw(c *ui.Ctx, l *draw.List, w, h float32) {
	t := s.hits.Texts()
	l.Image(draw.Rect{W: w, H: h}, areaImage, draw.Radii{}, 255)
	shade := draw.RGB(0x000000).WithAlpha(0x78)
	white := draw.RGB(0xffffff)
	if !s.have {
		l.Rect(draw.Rect{W: w, H: h}, shade, draw.Radii{})
	} else {
		r := s.rect()
		// Shade around the selection.
		l.Rect(draw.Rect{W: w, H: r.Y}, shade, draw.Radii{})
		l.Rect(draw.Rect{Y: r.Bottom(), W: w, H: h - r.Bottom()}, shade, draw.Radii{})
		l.Rect(draw.Rect{Y: r.Y, W: r.X, H: r.H}, shade, draw.Radii{})
		l.Rect(draw.Rect{X: r.Right(), Y: r.Y, W: w - r.Right(), H: r.H}, shade, draw.Radii{})
		l.Path(draw.Path{Points: []draw.Point{{X: r.X, Y: r.Y}, {X: r.Right(), Y: r.Y}, {X: r.Right(), Y: r.Bottom()}, {X: r.X, Y: r.Bottom()}},
			Closed: true, Stroke: white, Width: 1.5})
		size := fmt.Sprintf("%.0f × %.0f", r.W, r.H)
		lay := t.Lay(c, size, text.SansSemibold, 13)
		pill := draw.Rect{X: r.X, Y: r.Bottom() + 8, W: lay.Width + 20, H: 26}
		if pill.Bottom() > h-8 {
			pill.Y = r.Y - 34
		}
		l.Rect(pill, draw.RGB(0x1c1c1e).WithAlpha(0xe8), draw.Uniform(13))
		controls.Put(l, lay, pill.X+10, pill.Y+(pill.H-lay.Height)/2, white)
	}
	if !s.dragging {
		msg := "Drag over the part of the screen to record   ·   Esc cancels"
		lay := t.Lay(c, msg, text.SansSemibold, 14)
		pill := draw.Rect{X: (w - lay.Width - 36) / 2, Y: 60, W: lay.Width + 36, H: 38}
		l.Rect(pill, draw.RGB(0x1c1c1e).WithAlpha(0xe8), draw.Uniform(19))
		controls.Put(l, lay, pill.X+18, pill.Y+(pill.H-lay.Height)/2, white)
	}
}

func (s *areaScene) event(c *ui.Ctx, ev ui.Event) bool {
	switch ev.Kind {
	case ui.KeyPress:
		if ev.Key == ui.KeyEscape {
			s.close()
			return true
		}
	case ui.PointerDown:
		if ev.Button == 1 {
			s.dragging, s.have, s.from, s.to = true, true, ev.Pos, ev.Pos
			return true
		}
	case ui.PointerMove:
		crosshair()
		if s.dragging {
			s.to = ev.Pos
			return true
		}
	case ui.PointerUp:
		if ev.Button == 1 && s.dragging {
			s.dragging, s.to = false, ev.Pos
			r := s.rect()
			if r.W < minArea || r.H < minArea {
				s.have = false // a click, or too small: try again
				return true
			}
			s.c.setArea(s.display.ID, float64(r.X), float64(r.Y), float64(r.W), float64(r.H))
			s.c.show()
			s.close()
			return true
		}
	}
	return false
}

func (s *areaScene) animating() bool { return false }
func (s *areaScene) woke()           {}
