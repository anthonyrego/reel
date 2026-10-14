package icons

import (
	"testing"

	"github.com/anthonyrego/reel/internal/draw"
)

// Every icon draws something, and only inside its box (its lines' half
// width aside).
func TestInsideTheBox(t *testing.T) {
	const cx, cy, size = 100, 50, 24
	box := draw.Rect{X: cx - size/2, Y: cy - size/2, W: size, H: size}
	for name, icon := range All {
		var l draw.List
		icon(&l, cx, cy, size, draw.RGB(0xffffff))
		if len(l.Ops()) == 0 {
			t.Errorf("%s draws nothing", name)
		}
		in := func(p draw.Point, pad float32) bool {
			return p.X >= box.X-pad && p.X <= box.Right()+pad && p.Y >= box.Y-pad && p.Y <= box.Bottom()+pad
		}
		for _, op := range l.Ops() {
			switch op.Kind {
			case draw.OpPath:
				p := l.PathAt(op)
				for _, q := range p.Points {
					if !in(q, 0) {
						t.Errorf("%s: a point at %v is outside %v", name, q, box)
					}
				}
			case draw.OpRect:
				r := op.Rect
				if !in(draw.Point{X: r.X, Y: r.Y}, 0) || !in(draw.Point{X: r.Right(), Y: r.Bottom()}, 0) {
					t.Errorf("%s: a rect %v is outside %v", name, r, box)
				}
			}
		}
	}
}
