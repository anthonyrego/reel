package trim

import (
	"testing"
	"time"

	"github.com/anthonyrego/reel/internal/draw"
	"github.com/anthonyrego/reel/internal/text"
	"github.com/anthonyrego/reel/internal/theme"
	"github.com/anthonyrego/reel/internal/ui"
)

func ctx(t *testing.T) *ui.Ctx {
	t.Helper()
	s, err := text.NewShaper()
	if err != nil {
		t.Fatal(err)
	}
	return &ui.Ctx{Text: s, Pal: theme.Dark, Scale: 2, Now: time.Now()}
}

// rig is a view of a 10 s recording with every action noted.
type rig struct {
	t     *testing.T
	c     *ui.Ctx
	v     *View
	l     draw.List
	shown []time.Duration
	ran   map[string]int
	got   [2]time.Duration // the last Copy or Replace
}

func newRig(t *testing.T) *rig {
	r := &rig{t: t, c: ctx(t), ran: map[string]int{}}
	r.v = New(State{Name: "Recording", Duration: 10 * time.Second, Width: 1600, Height: 900, Frame: 2})
	r.v.Act = Actions{
		Show:    func(d time.Duration) { r.shown = append(r.shown, d) },
		Copy:    func(in, out time.Duration) { r.ran["copy"]++; r.got = [2]time.Duration{in, out} },
		Replace: func(in, out time.Duration) { r.ran["replace"]++; r.got = [2]time.Duration{in, out} },
		Cancel:  func() { r.ran["cancel"]++ },
	}
	r.draw()
	return r
}

func (r *rig) draw() {
	r.l.Reset()
	r.v.Draw(r.c, &r.l, 720, 640)
}

func (r *rig) ev(ev ui.Event) {
	r.v.Event(r.c, ev)
	r.draw()
}

// drag presses at from, moves to to and lets go.
func (r *rig) drag(from, to draw.Point) {
	r.ev(ui.Event{Kind: ui.PointerDown, Pos: from, Button: 1})
	r.ev(ui.Event{Kind: ui.PointerMove, Pos: to})
	r.ev(ui.Event{Kind: ui.PointerUp, Pos: to, Button: 1})
}

// click clicks the i-th control drawn: 0 Cancel, 1 Save as copy (or
// Replace, asking again), 2 Replace… (or Keep).
func (r *rig) click(i int) {
	rects := r.v.hits.Rects()
	if i >= len(rects) {
		r.t.Fatalf("%d controls drawn", len(rects))
	}
	b := rects[i]
	p := draw.Point{X: b.X + b.W/2, Y: b.Y + b.H/2}
	r.ev(ui.Event{Kind: ui.PointerMove, Pos: p})
	r.ev(ui.Event{Kind: ui.PointerDown, Pos: p, Button: 1})
	r.ev(ui.Event{Kind: ui.PointerUp, Pos: p, Button: 1})
}

func (r *rig) key(k ui.Key, mods ui.Mods) { r.ev(ui.Event{Kind: ui.KeyPress, Key: k, Mods: mods}) }

func center(r draw.Rect) draw.Point { return draw.Point{X: r.X + r.W/2, Y: r.Y + r.H/2} }

// near reports whether a is within a pixel's time of b (10 s across the
// track).
func near(a, b time.Duration) bool { return (a - b).Abs() <= 30*time.Millisecond }

func TestDragHandles(t *testing.T) {
	r := newRig(t)
	v := r.v
	mid := v.track.Y + v.track.H/2
	r.drag(center(v.handleRect(start)), draw.Point{X: v.x(2 * time.Second), Y: mid})
	r.drag(center(v.handleRect(end)), draw.Point{X: v.x(7 * time.Second), Y: mid})
	in, out := v.Range()
	if !near(in, 2*time.Second) || !near(out, 7*time.Second) {
		t.Errorf("range %v..%v, want 2s..7s", in, out)
	}
	if n := len(r.shown); n != 2 || !near(r.shown[0], 2*time.Second) || !near(r.shown[1], 7*time.Second) {
		t.Errorf("showed %v", r.shown)
	}

	// The handles stay minKeep apart, inside the recording.
	r.drag(center(v.handleRect(start)), draw.Point{X: v.x(9 * time.Second), Y: mid})
	if in, out := v.Range(); out-in != minKeep {
		t.Errorf("start dragged past the end: %v..%v", in, out)
	}
	r.drag(center(v.handleRect(end)), draw.Point{X: v.track.Right() + 200, Y: mid})
	if _, out := v.Range(); out != 10*time.Second {
		t.Errorf("end dragged past the track: %v", out)
	}
	r.drag(center(v.handleRect(start)), draw.Point{X: v.track.X - 200, Y: mid})
	if in, _ := v.Range(); in != 0 {
		t.Errorf("start dragged before the track: %v", in)
	}
}

// A press on the track moves the nearer handle there.
func TestClickTrack(t *testing.T) {
	r := newRig(t)
	v := r.v
	mid := v.track.Y + v.track.H/2
	p := draw.Point{X: v.x(8 * time.Second), Y: mid}
	r.drag(p, p)
	if in, out := v.Range(); in != 0 || !near(out, 8*time.Second) {
		t.Errorf("click at 8s: %v..%v", in, out)
	}
	p = draw.Point{X: v.x(3 * time.Second), Y: mid}
	r.drag(p, p)
	if in, _ := v.Range(); !near(in, 3*time.Second) {
		t.Errorf("click at 3s: in %v", in)
	}
}

func TestSave(t *testing.T) {
	r := newRig(t)
	// Nothing cut yet: neither saves.
	r.click(1)
	r.click(2)
	r.key(ui.KeyEnter, 0)
	if r.ran["copy"] != 0 || r.ran["replace"] != 0 || r.v.confirm {
		t.Fatalf("saved with nothing cut: %v", r.ran)
	}
	r.key(ui.KeyArrowRight, ui.Shift) // the start, a second in
	if in, _ := r.v.Range(); in != time.Second {
		t.Fatalf("start %v after shift-→", in)
	}
	r.click(1)
	if r.ran["copy"] != 1 || r.got != [2]time.Duration{time.Second, 10 * time.Second} {
		t.Errorf("Save as copy: %v %v", r.ran, r.got)
	}
	// Replace asks again; Keep takes it back, Replace does it.
	r.click(2)
	if !r.v.confirm || r.ran["replace"] != 0 {
		t.Fatalf("Replace… didn't ask: %v", r.ran)
	}
	r.click(2) // Keep
	if r.v.confirm || r.ran["replace"] != 0 {
		t.Fatalf("Keep: %v", r.ran)
	}
	r.click(2)
	r.click(1) // Replace
	if r.ran["replace"] != 1 || r.got != [2]time.Duration{time.Second, 10 * time.Second} {
		t.Errorf("Replace: %v %v", r.ran, r.got)
	}
	r.key(ui.KeyEnter, 0)
	if r.ran["copy"] != 2 {
		t.Errorf("Return didn't save a copy: %v", r.ran)
	}
}

func TestKeys(t *testing.T) {
	r := newRig(t)
	v := r.v
	mid := v.track.Y + v.track.H/2
	r.drag(center(v.handleRect(end)), draw.Point{X: v.x(5 * time.Second), Y: mid})
	_, out := v.Range()
	r.key(ui.KeyArrowLeft, 0) // the handle last moved
	if _, o := v.Range(); o != out-nudge {
		t.Errorf("← moved the end %v → %v", out, o)
	}
	r.key(ui.KeyArrowLeft, ui.Shift)
	if _, o := v.Range(); o != out-nudge-bigNudge || r.shown[len(r.shown)-1] != o-time.Millisecond {
		t.Errorf("shift-← moved the end to %v (shown %v)", o, r.shown[len(r.shown)-1])
	}
	r.click(2) // Replace…: Esc takes the question back first
	r.key(ui.KeyEscape, 0)
	if v.confirm || r.ran["cancel"] != 0 {
		t.Fatalf("Esc while asking: confirm %v, %v", v.confirm, r.ran)
	}
	r.key(ui.KeyEscape, 0)
	if r.ran["cancel"] != 1 {
		t.Errorf("Esc didn't cancel: %v", r.ran)
	}
	r.click(0)
	if r.ran["cancel"] != 2 {
		t.Errorf("Cancel: %v", r.ran)
	}
}

// While the copy is written nothing changes or runs.
func TestBusy(t *testing.T) {
	r := newRig(t)
	v := r.v
	r.key(ui.KeyArrowRight, ui.Shift)
	v.St.Busy = "Saving…"
	r.draw()
	in, out := v.Range()
	mid := v.track.Y + v.track.H/2
	r.drag(center(v.handleRect(start)), draw.Point{X: v.x(5 * time.Second), Y: mid})
	r.key(ui.KeyArrowRight, 0)
	for i := range 3 {
		r.click(i)
	}
	r.key(ui.KeyEnter, 0)
	r.key(ui.KeyEscape, 0)
	if i, o := v.Range(); i != in || o != out || len(r.ran) != 0 {
		t.Errorf("busy: %v..%v, ran %v", i, o, r.ran)
	}
}

func TestFit(t *testing.T) {
	box := draw.Rect{X: 10, Y: 20, W: 400, H: 100}
	if got := fit(box, 1920, 1080); got.H != 100 || got.W < 177 || got.W > 178 || got.Y != 20 {
		t.Errorf("wide box: %+v", got)
	}
	if got := fit(draw.Rect{W: 100, H: 400}, 1920, 1080); got.W != 100 || got.Y < 171 || got.Y > 172 {
		t.Errorf("tall box: %+v", got)
	}
}

func TestFormats(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0: "0:00.0", 2*time.Second + 340*time.Millisecond: "0:02.3", 61*time.Second + 960*time.Millisecond: "1:02.0",
		time.Hour + 5*time.Second: "1:00:05.0",
	} {
		if got := Stamp(d); got != want {
			t.Errorf("Stamp(%v) = %q, want %q", d, got, want)
		}
	}
	for d, want := range map[time.Duration]string{
		3 * time.Second: "3.0 s", 90 * time.Second: "1:30", time.Hour + time.Minute: "1:01:00",
	} {
		if got := Length(d); got != want {
			t.Errorf("Length(%v) = %q, want %q", d, got, want)
		}
	}
}
