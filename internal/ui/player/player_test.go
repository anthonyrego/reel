package player

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

// seek is a Seek the view asked for.
type seek struct {
	t     time.Duration
	exact bool
}

// rig is a view of a 20 s recording, 4 s in, playing, that plays along:
// Play, Pause, Seek and SetMuted change its State as the app's would.
type rig struct {
	t     *testing.T
	c     *ui.Ctx
	v     *View
	l     draw.List
	ran   []string
	seeks []seek
}

func newRig(t *testing.T) *rig {
	r := &rig{t: t, c: ctx(t)}
	r.v = New(State{Name: "Recording", Duration: 20 * time.Second, Width: 1600, Height: 900, Frame: 5, Time: 4 * time.Second, Playing: true})
	note := func(name string, then func()) func() {
		return func() { r.ran = append(r.ran, name); then() }
	}
	st := &r.v.St
	r.v.Act = Actions{
		Play:     note("play", func() { st.Playing = true }),
		Pause:    note("pause", func() { st.Playing = false }),
		Seek:     func(d time.Duration, exact bool) { r.seeks = append(r.seeks, seek{d, exact}); st.Time = d },
		SetMuted: func(m bool) { r.ran = append(r.ran, "mute"); st.Muted = m },
		Copy:     note("copy", func() {}),
		Trim:     note("trim", func() {}),
		Reveal:   note("reveal", func() {}),
		Back:     note("back", func() {}),
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

func (r *rig) key(k ui.Key) { r.ev(ui.Event{Kind: ui.KeyPress, Key: k}) }

func (r *rig) clickAt(p draw.Point) {
	r.ev(ui.Event{Kind: ui.PointerMove, Pos: p})
	r.ev(ui.Event{Kind: ui.PointerDown, Pos: p, Button: 1})
	r.ev(ui.Event{Kind: ui.PointerUp, Pos: p, Button: 1})
}

// took is what ran since the last call.
func (r *rig) took() []string {
	ran := r.ran
	r.ran = nil
	return ran
}

func same(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// near reports whether a is within a pixel's time of b (20 s across about
// 400 points of bar).
func near(a, b time.Duration) bool { return (a - b).Abs() <= 60*time.Millisecond }

func center(r draw.Rect) draw.Point { return draw.Point{X: r.X + r.W/2, Y: r.Y + r.H/2} }

// Every control does its one thing, in the order drawn: Back, the picture
// (pause), play (it's paused now), the sound, then copy, trim and show in
// Finder from the right.
func TestEveryControl(t *testing.T) {
	r := newRig(t)
	for _, b := range r.v.hits.Rects() {
		r.clickAt(center(b))
	}
	want := []string{"back", "pause", "play", "copy", "trim", "reveal", "mute"}
	got := r.took()
	seen := map[string]int{}
	for _, g := range got {
		seen[g]++
	}
	for _, w := range want {
		if seen[w] != 1 {
			t.Errorf("%s ran %d times: %v", w, seen[w], got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("ran %v, want each of %v once", got, want)
	}
	if !r.v.St.Muted {
		t.Error("the sound button didn't mute")
	}
}

// Space plays and pauses, M mutes and unmutes, Esc goes back.
func TestKeys(t *testing.T) {
	r := newRig(t)
	r.key(' ')
	r.key(' ')
	r.key('m')
	r.key('m')
	r.key(ui.KeyEscape)
	if got := r.took(); !same(got, []string{"pause", "play", "mute", "mute", "back"}) {
		t.Errorf("ran %v", got)
	}
}

// The arrows skip 5 s, exactly, inside the recording; Home goes to the
// start.
func TestSkip(t *testing.T) {
	r := newRig(t)
	r.key(ui.KeyArrowRight)
	r.key(ui.KeyArrowLeft)
	r.key(ui.KeyArrowLeft)
	r.v.St.Time = 18 * time.Second
	r.key(ui.KeyArrowRight)
	r.v.St.Time = 9 * time.Second
	r.key(ui.KeyHome)
	want := []seek{{9 * time.Second, true}, {4 * time.Second, true}, {0, true}, {20 * time.Second, true}, {0, true}}
	if len(r.seeks) != len(want) {
		t.Fatalf("seeks %v, want %v", r.seeks, want)
	}
	for i := range want {
		if r.seeks[i] != want[i] {
			t.Errorf("seek %d = %v, want %v", i, r.seeks[i], want[i])
		}
	}
}

// A drag along the bar pauses, follows the pointer loosely and lands
// exactly where it's let go, then plays again; the screen shows the
// drag's time meanwhile.
func TestDrag(t *testing.T) {
	r := newRig(t)
	bar := r.v.Bar()
	mid := bar.Y + bar.H/2
	at := func(f float32) draw.Point { return draw.Point{X: bar.X + bar.W*f, Y: mid} }
	r.ev(ui.Event{Kind: ui.PointerDown, Pos: at(0.25), Button: 1})
	if got := r.took(); !same(got, []string{"pause"}) {
		t.Errorf("the press ran %v, want a pause", got)
	}
	r.v.St.Time = 4 * time.Second // the player hasn't caught up
	r.ev(ui.Event{Kind: ui.PointerMove, Pos: at(0.5)})
	if !r.v.Dragging() || !near(r.v.Shown(), 10*time.Second) {
		t.Errorf("mid-drag: dragging %v, shows %v, want 10 s", r.v.Dragging(), r.v.Shown())
	}
	r.ev(ui.Event{Kind: ui.PointerMove, Pos: at(1.3)}) // past the end
	r.ev(ui.Event{Kind: ui.PointerUp, Pos: at(0.75), Button: 1})
	if got := r.took(); !same(got, []string{"play"}) {
		t.Errorf("the release ran %v, want play", got)
	}
	if len(r.seeks) != 4 {
		t.Fatalf("seeks %v", r.seeks)
	}
	for i, w := range []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second} {
		if s := r.seeks[i]; s.exact || !near(s.t, w) {
			t.Errorf("seek %d = %v, want loosely to %v", i, s, w)
		}
	}
	// The release lands where the drag was last, not where it's let go.
	if s := r.seeks[3]; !s.exact || s.t != 20*time.Second {
		t.Errorf("the release's seek = %v, want exactly to 20 s", s)
	}
}

// A drag while paused stays paused.
func TestDragPaused(t *testing.T) {
	r := newRig(t)
	r.v.St.Playing = false
	bar := r.v.Bar()
	p := draw.Point{X: bar.X + bar.W/2, Y: bar.Y + bar.H/2}
	r.ev(ui.Event{Kind: ui.PointerDown, Pos: p, Button: 1})
	r.ev(ui.Event{Kind: ui.PointerUp, Pos: p, Button: 1})
	if got := r.took(); len(got) != 0 {
		t.Errorf("ran %v, want nothing but seeks", got)
	}
	if len(r.seeks) != 2 || !r.seeks[1].exact {
		t.Errorf("seeks %v", r.seeks)
	}
}

// Nothing plays before the length is known.
func TestNotLoaded(t *testing.T) {
	r := newRig(t)
	r.v.St = State{Name: "Recording"}
	r.draw()
	r.key(' ')
	bar := r.v.Bar()
	r.clickAt(draw.Point{X: bar.X + bar.W/2, Y: bar.Y + bar.H/2})
	if len(r.ran) != 0 || len(r.seeks) != 0 {
		t.Errorf("ran %v, seeks %v", r.ran, r.seeks)
	}
}

func TestFormats(t *testing.T) {
	for _, c := range []struct {
		d             time.Duration
		clock, length string
	}{
		{0, "0:00", "0:00"},
		{1600 * time.Millisecond, "0:01", "0:02"},
		{47300 * time.Millisecond, "0:47", "0:47"},
		{59600 * time.Millisecond, "0:59", "1:00"},
		{3*time.Hour + 2*time.Minute + 5*time.Second, "3:02:05", "3:02:05"},
	} {
		if got := Clock(c.d); got != c.clock {
			t.Errorf("Clock(%v) = %q, want %q", c.d, got, c.clock)
		}
		if got := Length(c.d); got != c.length {
			t.Errorf("Length(%v) = %q, want %q", c.d, got, c.length)
		}
	}
}
