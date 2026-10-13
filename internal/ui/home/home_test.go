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
	return &ui.Ctx{Text: s, Pal: theme.For(false), Scale: 2, Now: time.Now()}
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

// clickAll clicks the middle of every control drawn, redrawing between
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
	}
	var l draw.List
	v.Draw(c, &l, 720, 640)
	for _, r := range v.hits.Rects() {
		if r.W > 1e4 {
			continue // a menu's backdrop
		}
		p := draw.Point{X: r.X + r.W/2, Y: r.Y + r.H/2}
		v.Event(c, ui.Event{Kind: ui.PointerMove, Pos: p})
		v.Event(c, ui.Event{Kind: ui.PointerDown, Pos: p, Button: 1})
		v.Event(c, ui.Event{Kind: ui.PointerUp, Pos: p, Button: 1})
		l.Reset()
		v.Draw(c, &l, 720, 640)
		v.menu, v.confirm = "", ""
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
	if ran["pause"] != 1 || ran["stop"] != 1 || ran["record"] != 0 || ran["target"] != 0 {
		t.Errorf("%v", ran)
	}
}

// Delete asks again before it runs.
func TestDeleteAsks(t *testing.T) {
	v := New()
	v.St = state()
	c := ctx(t)
	deleted := ""
	v.Act = Actions{Delete: func(p string) { deleted = p }}
	var l draw.List
	click := func(label string) bool {
		v.Draw(c, &l, 720, 640)
		// Hover the row so its quiet buttons are drawn.
		row := draw.Point{X: 100, Y: v.listTop + 20}
		v.Event(c, ui.Event{Kind: ui.PointerMove, Pos: row})
		l.Reset()
		v.Draw(c, &l, 720, 640)
		for _, r := range v.hits.Rects() {
			if r.Y < v.listTop || r.W > 300 {
				continue
			}
			p := draw.Point{X: r.X + r.W/2, Y: r.Y + r.H/2}
			v.Event(c, ui.Event{Kind: ui.PointerDown, Pos: p, Button: 1})
			v.Event(c, ui.Event{Kind: ui.PointerUp, Pos: p, Button: 1})
			if v.confirm != "" || deleted != "" {
				return true
			}
		}
		return false
	}
	if !click("Delete") || deleted != "" || v.confirm != "/a.mp4" {
		t.Fatalf("first click: confirm %q deleted %q", v.confirm, deleted)
	}
	// Now the row shows Delete (danger) and Keep; Delete is the rightmost.
	l.Reset()
	v.Draw(c, &l, 720, 640)
	var rects []draw.Rect
	for _, r := range v.hits.Rects() {
		if r.Y >= v.listTop && r.W < 300 {
			rects = append(rects, r)
		}
	}
	if len(rects) < 2 {
		t.Fatalf("%d buttons on the row", len(rects))
	}
	del := rects[0]
	p := draw.Point{X: del.X + del.W/2, Y: del.Y + del.H/2}
	v.Event(c, ui.Event{Kind: ui.PointerDown, Pos: p, Button: 1})
	v.Event(c, ui.Event{Kind: ui.PointerUp, Pos: p, Button: 1})
	if deleted != "/a.mp4" {
		t.Errorf("deleted %q", deleted)
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
