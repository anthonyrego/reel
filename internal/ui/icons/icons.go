// Package icons draws reel's small line icons into a draw.List: what to
// record (a display, a window, an area), the sound (a mic, the speaker),
// what can be done with a recording (copy, trim, show in Finder, delete)
// and the transport (play, pause, stop). Each is drawn on a 24-unit grid
// scaled into a size×size box around a center, in lines of one weight, so
// they sit together. Pure.
package icons

import (
	"math"

	"github.com/anthonyrego/reel/internal/draw"
)

// Icon draws an icon centered on cx, cy, size units across, in col.
type Icon func(l *draw.List, cx, cy, size float32, col draw.Color)

// pen draws on the 24-unit grid, centered on 12, 12.
type pen struct {
	l      *draw.List
	cx, cy float32
	u      float32 // logical units per grid unit
	col    draw.Color
}

func newPen(l *draw.List, cx, cy, size float32, col draw.Color) pen {
	return pen{l: l, cx: cx, cy: cy, u: size / 24, col: col}
}

func (p pen) pt(x, y float32) draw.Point {
	return draw.Point{X: p.cx + (x-12)*p.u, Y: p.cy + (y-12)*p.u}
}

// width is the line weight: 1.8 grid units, never under 1.2.
func (p pen) width() float32 { return max(1.2, 1.8*p.u) }

func (p pen) points(xy []float32) []draw.Point {
	pts := make([]draw.Point, 0, len(xy)/2)
	for i := 0; i+1 < len(xy); i += 2 {
		pts = append(pts, p.pt(xy[i], xy[i+1]))
	}
	return pts
}

// line strokes a polyline through x, y pairs.
func (p pen) line(xy ...float32) {
	p.l.Path(draw.Path{Points: p.points(xy), Stroke: p.col, Width: p.width()})
}

// ring strokes a closed polygon.
func (p pen) ring(xy ...float32) {
	p.l.Path(draw.Path{Points: p.points(xy), Closed: true, Stroke: p.col, Width: p.width()})
}

// fill fills a closed polygon.
func (p pen) fill(xy ...float32) {
	p.l.Path(draw.Path{Points: p.points(xy), Closed: true, Fill: p.col})
}

// box strokes a rounded rect from x0, y0 to x1, y1.
func (p pen) box(x0, y0, x1, y1, r float32) {
	a, b := p.pt(x0, y0), p.pt(x1, y1)
	p.l.BorderedRect(draw.Rect{X: a.X, Y: a.Y, W: b.X - a.X, H: b.Y - a.Y}, draw.Color{}, p.col, p.width(), draw.Uniform(r*p.u))
}

// solid fills a rounded rect.
func (p pen) solid(x0, y0, x1, y1, r float32) {
	a, b := p.pt(x0, y0), p.pt(x1, y1)
	p.l.Rect(draw.Rect{X: a.X, Y: a.Y, W: b.X - a.X, H: b.Y - a.Y}, p.col, draw.Uniform(r*p.u))
}

// arc strokes part of a circle, from angle a0 to a1 in degrees (0 is +x,
// 90 is down: y grows down).
func (p pen) arc(x, y, r, a0, a1 float32) {
	p.l.Path(draw.Path{Points: p.points(arcXY(x, y, r, a0, a1)), Stroke: p.col, Width: p.width()})
}

// circle strokes a whole circle.
func (p pen) circle(x, y, r float32) {
	xy := arcXY(x, y, r, 0, 360)
	p.l.Path(draw.Path{Points: p.points(xy[:len(xy)-2]), Closed: true, Stroke: p.col, Width: p.width()})
}

// arcXY is points along an arc, one per 15 degrees or closer.
func arcXY(x, y, r, a0, a1 float32) []float32 {
	n := max(2, int(math.Ceil(float64(abs(a1-a0))/15)))
	xy := make([]float32, 0, 2*(n+1))
	for i := 0; i <= n; i++ {
		a := float64(a0+(a1-a0)*float32(i)/float32(n)) * math.Pi / 180
		xy = append(xy, x+r*float32(math.Cos(a)), y+r*float32(math.Sin(a)))
	}
	return xy
}

func abs(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

// Display is a monitor on a stand.
func Display(l *draw.List, cx, cy, size float32, col draw.Color) {
	p := newPen(l, cx, cy, size, col)
	p.box(2.5, 4, 21.5, 16.5, 2)
	p.line(12, 16.5, 12, 20)
	p.line(8, 20.5, 16, 20.5)
}

// Window is a window with its title bar.
func Window(l *draw.List, cx, cy, size float32, col draw.Color) {
	p := newPen(l, cx, cy, size, col)
	p.box(2.5, 4, 21.5, 20, 2.5)
	p.line(2.5, 9, 21.5, 9)
	p.solid(5, 5.6, 6.6, 7.2, 0.8)
	p.solid(7.8, 5.6, 9.4, 7.2, 0.8)
}

// Area is the four corners of a selection.
func Area(l *draw.List, cx, cy, size float32, col draw.Color) {
	p := newPen(l, cx, cy, size, col)
	p.line(3, 8, 3, 3, 8, 3)
	p.line(16, 3, 21, 3, 21, 8)
	p.line(21, 16, 21, 21, 16, 21)
	p.line(8, 21, 3, 21, 3, 16)
	p.solid(10, 10, 14, 14, 1)
}

// Mic is a microphone.
func Mic(l *draw.List, cx, cy, size float32, col draw.Color) {
	p := newPen(l, cx, cy, size, col)
	p.box(8.5, 2.5, 15.5, 14.5, 3.5)
	p.arc(12, 11, 6.5, 0, 180)
	p.line(12, 17.5, 12, 21)
}

// MicOff is a microphone struck through: none.
func MicOff(l *draw.List, cx, cy, size float32, col draw.Color) {
	Mic(l, cx, cy, size, col)
	newPen(l, cx, cy, size, col).line(3.5, 3.5, 20.5, 20.5)
}

func speakerBody(p pen) { p.ring(3, 9, 7, 9, 12, 4.5, 12, 19.5, 7, 15, 3, 15) }

// Speaker is a speaker sounding: system audio on.
func Speaker(l *draw.List, cx, cy, size float32, col draw.Color) {
	p := newPen(l, cx, cy, size, col)
	speakerBody(p)
	p.arc(12, 12, 4, -45, 45)
	p.arc(12, 12, 8, -48, 48)
}

// SpeakerOff is a speaker with a cross: system audio off.
func SpeakerOff(l *draw.List, cx, cy, size float32, col draw.Color) {
	p := newPen(l, cx, cy, size, col)
	speakerBody(p)
	p.line(15.5, 9, 21, 14.5)
	p.line(21, 9, 15.5, 14.5)
}

// Copy is two sheets, one over the other.
func Copy(l *draw.List, cx, cy, size float32, col draw.Color) {
	p := newPen(l, cx, cy, size, col)
	p.box(8.5, 8.5, 20.5, 20.5, 2.5)
	p.line(4, 15.5, 4, 6, 6, 4, 15.5, 4)
}

// Trim is a pair of scissors.
func Trim(l *draw.List, cx, cy, size float32, col draw.Color) {
	p := newPen(l, cx, cy, size, col)
	p.circle(6, 6, 3)
	p.circle(6, 18, 3)
	p.line(20.5, 4, 8.2, 15.8)
	p.line(14.5, 14.5, 20.5, 20)
	p.line(8.2, 8.2, 11.5, 11.5)
}

// Reveal is a magnifying glass: show in Finder.
func Reveal(l *draw.List, cx, cy, size float32, col draw.Color) {
	p := newPen(l, cx, cy, size, col)
	p.circle(10.5, 10.5, 6.5)
	p.line(15.3, 15.3, 20.5, 20.5)
}

// Trash is a bin with its lid.
func Trash(l *draw.List, cx, cy, size float32, col draw.Color) {
	p := newPen(l, cx, cy, size, col)
	p.line(3.5, 6, 20.5, 6)
	p.line(9, 6, 9, 3.5, 15, 3.5, 15, 6)
	p.line(5.5, 6, 6.5, 20.5, 17.5, 20.5, 18.5, 6)
	p.line(10, 10.5, 10, 16.5)
	p.line(14, 10.5, 14, 16.5)
}

// Folder is a folder.
func Folder(l *draw.List, cx, cy, size float32, col draw.Color) {
	p := newPen(l, cx, cy, size, col)
	p.ring(2.5, 5, 9.5, 5, 11.5, 7.5, 21.5, 7.5, 21.5, 19.5, 2.5, 19.5)
}

// Check is a check mark.
func Check(l *draw.List, cx, cy, size float32, col draw.Color) {
	newPen(l, cx, cy, size, col).line(4.5, 12.5, 9.5, 17.5, 19.5, 6.5)
}

// ChevronDown points down: a menu.
func ChevronDown(l *draw.List, cx, cy, size float32, col draw.Color) {
	newPen(l, cx, cy, size, col).line(6.5, 9.5, 12, 15, 17.5, 9.5)
}

// ChevronLeft points left: back.
func ChevronLeft(l *draw.List, cx, cy, size float32, col draw.Color) {
	newPen(l, cx, cy, size, col).line(15, 5.5, 8.5, 12, 15, 18.5)
}

// Warning is a triangle with an exclamation mark.
func Warning(l *draw.List, cx, cy, size float32, col draw.Color) {
	p := newPen(l, cx, cy, size, col)
	p.ring(12, 3, 21.5, 20, 2.5, 20)
	p.line(12, 9.5, 12, 14)
	p.solid(11, 16, 13, 18, 1)
}

// Play is a solid triangle pointing right.
func Play(l *draw.List, cx, cy, size float32, col draw.Color) {
	newPen(l, cx, cy, size, col).fill(7, 4.5, 19.5, 12, 7, 19.5)
}

// Pause is two solid bars.
func Pause(l *draw.List, cx, cy, size float32, col draw.Color) {
	p := newPen(l, cx, cy, size, col)
	p.solid(6, 4.5, 10, 19.5, 1.5)
	p.solid(14, 4.5, 18, 19.5, 1.5)
}

// Stop is a solid rounded square.
func Stop(l *draw.List, cx, cy, size float32, col draw.Color) {
	newPen(l, cx, cy, size, col).solid(5, 5, 19, 19, 3)
}

// Film is a frame of film: a picture still to come.
func Film(l *draw.List, cx, cy, size float32, col draw.Color) {
	p := newPen(l, cx, cy, size, col)
	p.box(2.5, 4, 21.5, 20, 2.5)
	p.line(7, 4, 7, 20)
	p.line(17, 4, 17, 20)
	p.line(2.5, 12, 7, 12)
	p.line(17, 12, 21.5, 12)
}

// All are every icon by name (tests, renders).
var All = map[string]Icon{
	"display": Display, "window": Window, "area": Area,
	"mic": Mic, "mic-off": MicOff, "speaker": Speaker, "speaker-off": SpeakerOff,
	"copy": Copy, "trim": Trim, "reveal": Reveal, "trash": Trash, "folder": Folder,
	"check": Check, "chevron-down": ChevronDown, "chevron-left": ChevronLeft, "warning": Warning,
	"play": Play, "pause": Pause, "stop": Stop, "film": Film,
}
