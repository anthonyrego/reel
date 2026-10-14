package home

import (
	"testing"
	"time"

	"github.com/anthonyrego/reel/internal/draw"
	"github.com/anthonyrego/reel/internal/text"
	"github.com/anthonyrego/reel/internal/theme"
	"github.com/anthonyrego/reel/internal/ui"
	"github.com/anthonyrego/reel/internal/ui/controls"
)

func ctx(t *testing.T) *ui.Ctx {
	t.Helper()
	s, err := text.NewShaper()
	if err != nil {
		t.Fatal(err)
	}
	return &ui.Ctx{Text: s, Pal: theme.Dark, Scale: 2, Now: time.Now()}
}

func state() State {
	return State{
		Target:      Display,
		Displays:    []controls.Choice{{ID: "1", Label: "Built-in"}},
		Display:     "1",
		Mics:        []controls.Choice{{ID: NoMic, Label: "No microphone"}, {ID: "m", Label: "Mic"}},
		SystemAudio: true,
		Recordings:  []Recording{{Path: "/a.mp4", Name: "a", Duration: time.Second, Size: 1000, When: time.Now()}},
	}
}

// clickAll clicks the middle of every control drawn, with the pointer
// over each card in turn (its buttons show only then), redrawing between
// clicks, and returns what ran.
func clickAll(t *testing.T, v *View) map[string]int {
	t.Helper()
	c := ctx(t)
	ran := map[string]int{}
	note := func(name string) func() { return func() { ran[name]++ } }
	v.Act = Actions{
		Record: note("record"), Pause: note("pause"), Resume: note("resume"), Stop: note("stop"),
		AskScreen: note("askScreen"), AskMic: note("askMic"), SelectArea: note("selectArea"), OpenFolder: note("folder"),
		SetTarget:      func(Target) { ran["target"]++ },
		PickDisplay:    func(string) { ran["display"]++ },
		PickMic:        func(string) { ran["mic"]++ },
		SetSystemAudio: func(bool) { ran["systemAudio"]++ },
		Copy:           func(string) { ran["copy"]++ },
		Reveal:         func(string) { ran["reveal"]++ },
		Delete:         func(string) { ran["delete"]++ },
		Trim:           func(string) { ran["trim"]++ },
	}
	var l draw.List
	draw1 := func() { l.Reset(); v.Draw(c, &l, 720, 640) }
	draw1()
	hovers := []draw.Point{{X: 1, Y: 1}} // off every card
	for _, r := range v.St.Recordings {
		if card, ok := v.Card(r.Path); ok {
			hovers = append(hovers, draw.Point{X: card.X + 4, Y: card.Bottom() - 4})
		}
	}
	seen := map[draw.Rect]bool{}
	for _, at := range hovers {
		v.Event(c, ui.Event{Kind: ui.PointerMove, Pos: at})
		draw1()
		for _, r := range v.hits.Rects() {
			if r.W > 1e4 || seen[r] {
				continue // a menu's backdrop, or clicked already
			}
			seen[r] = true
			p := draw.Point{X: r.X + r.W/2, Y: r.Y + r.H/2}
			v.Event(c, ui.Event{Kind: ui.PointerMove, Pos: p})
			v.Event(c, ui.Event{Kind: ui.PointerDown, Pos: p, Button: 1})
			v.Event(c, ui.Event{Kind: ui.PointerUp, Pos: p, Button: 1})
			v.menu, v.confirm = "", ""
			v.Event(c, ui.Event{Kind: ui.PointerMove, Pos: at})
			draw1()
		}
	}
	return ran
}

func TestRecordNeedsScreenRecording(t *testing.T) {
	v := New()
	v.St = state()
	if ran := clickAll(t, v); ran["record"] != 1 || ran["askScreen"] != 0 {
		t.Errorf("with the permission: %v", ran)
	}
	v.St.Screen = PermAsk
	if ran := clickAll(t, v); ran["record"] != 0 || ran["askScreen"] != 1 {
		t.Errorf("without it: %v", ran)
	}
}

func TestLiveShowsPauseAndStop(t *testing.T) {
	v := New()
	v.St = state()
	v.St.Live = &Live{Elapsed: time.Minute}
	ran := clickAll(t, v)
	if ran["pause"] != 1 || ran["stop"] != 1 || ran["record"] != 0 || ran["target"] != 0 || ran["systemAudio"] != 0 {
		t.Errorf("%v", ran)
	}
}

// Every control in the bar and on a card does its one thing; a click on
// the card itself copies it.
func TestEveryControl(t *testing.T) {
	v := New()
	v.St = state()
	ran := clickAll(t, v)
	for _, name := range []string{"record", "systemAudio", "folder", "trim", "reveal"} {
		if ran[name] != 1 {
			t.Errorf("%s ran %d times: %v", name, ran[name], ran)
		}
	}
	// The mode's other two choices; Copy's button and the card itself.
	if ran["target"] != 2 || ran["copy"] != 2 {
		t.Errorf("%v", ran)
	}
	if ran["delete"] != 0 {
		t.Error("Delete didn't ask first")
	}
}

// Delete asks again (over the card's picture) before it runs.
func TestDeleteAsks(t *testing.T) {
	v := New()
	v.St = state()
	c := ctx(t)
	deleted := ""
	v.Act = Actions{Delete: func(p string) { deleted = p }, Copy: func(string) {}}
	var l draw.List
	v.Draw(c, &l, 720, 640)
	card, ok := v.Card("/a.mp4")
	if !ok {
		t.Fatal("the card isn't drawn")
	}
	at := draw.Point{X: card.X + 4, Y: card.Bottom() - 4}
	v.Event(c, ui.Event{Kind: ui.PointerMove, Pos: at})
	l.Reset()
	v.Draw(c, &l, 720, 640)
	click := func(p draw.Point) {
		v.Event(c, ui.Event{Kind: ui.PointerMove, Pos: p})
		v.Event(c, ui.Event{Kind: ui.PointerDown, Pos: p, Button: 1})
		v.Event(c, ui.Event{Kind: ui.PointerUp, Pos: p, Button: 1})
		l.Reset()
		v.Draw(c, &l, 720, 640)
	}
	// The trash is the rightmost button over the picture.
	click(draw.Point{X: card.Right() - 8 - 14, Y: card.Y + 8 + 14})
	if deleted != "" || v.confirm != "/a.mp4" {
		t.Fatalf("first click: confirm %q deleted %q", v.confirm, deleted)
	}
	// Now Keep and Delete; Delete is the last drawn.
	rects := v.hits.Rects()
	del := rects[len(rects)-1]
	click(draw.Point{X: del.X + del.W/2, Y: del.Y + del.H/2})
	if deleted != "/a.mp4" {
		t.Errorf("deleted %q", deleted)
	}
}

// The cards fill the width: two at the narrowest, more as it grows.
func TestColumns(t *testing.T) {
	for _, tc := range []struct {
		w    float32
		want int
	}{{400, 2}, {640, 2}, {720, 3}, {800, 3}, {1200, 5}} {
		if got := Columns(tc.w); got != tc.want {
			t.Errorf("Columns(%v) = %d, want %d", tc.w, got, tc.want)
		}
	}
}

// A card says when reel's recordings were made (and that a trim is one),
// and anything renamed by its name.
func TestTitle(t *testing.T) {
	now := time.Date(2026, 10, 13, 22, 30, 0, 0, time.Local)
	at := time.Date(2026, 10, 13, 22, 4, 2, 0, time.Local)
	for name, want := range map[string]string{
		"Recording 2026-10-13 at 22.04.02":           "10:04 PM",
		"Recording 2026-10-13 at 22.04.02 2":         "10:04 PM",
		"Recording 2026-10-13 at 22.04.02 trimmed":   "10:04 PM · Trimmed",
		"Recording 2026-10-13 at 22.04.02 trimmed 3": "10:04 PM · Trimmed",
		"Demo for Sam":                           "Demo for Sam",
		"Recording 2026-10-13 at 22.04.02 final": "Recording 2026-10-13 at 22.04.02 final",
	} {
		if got := Title(Recording{Name: name, When: at}, now); got != want {
			t.Errorf("%q: %q, want %q", name, got, want)
		}
	}
}

func TestFormats(t *testing.T) {
	if Clock(83*time.Second) != "1:23" || Clock(3725*time.Second) != "1:02:05" {
		t.Error("Clock")
	}
	if Size(999) != "1 KB" || Size(24_800_000) != "24.8 MB" || Size(2_100_000_000) != "2.1 GB" {
		t.Error("Size")
	}
	now := time.Date(2026, 10, 13, 15, 0, 0, 0, time.Local)
	if got := When(now.Add(-time.Hour), now); got != "2:00 PM" {
		t.Errorf("today: %s", got)
	}
	if got := When(now.Add(-20*time.Hour), now); got != "Yesterday 7:00 PM" {
		t.Errorf("yesterday: %s", got)
	}
}
