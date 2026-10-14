package main

import (
	"testing"

	"github.com/anthonyrego/reel/internal/config"
	"github.com/anthonyrego/reel/internal/mac"
	"github.com/anthonyrego/reel/internal/ui/home"
)

// Every control shows what it did at once, not at the loop's next wake.
// (Only actions that touch no device, the screen or the clipboard.)
func TestActionsShow(t *testing.T) {
	v := home.New()
	c := newController(config.Paths{Data: t.TempDir(), Recordings: t.TempDir()}, v)
	c.mics = []mac.Mic{{ID: "m", Name: "Mic"}}
	c.show()

	v.Act.SetTarget(home.Area)
	if v.St.Target != home.Area {
		t.Errorf("chose an area: the view shows %q", v.St.Target)
	}
	v.Act.SetTarget(home.Display)
	if v.St.Target != home.Display {
		t.Errorf("chose a display: the view shows %q", v.St.Target)
	}
	v.Act.PickDisplay("7")
	if v.St.Display != "7" {
		t.Errorf("picked display 7: the view shows %q", v.St.Display)
	}
	v.Act.PickWindow("9")
	if v.St.Window != "9" {
		t.Errorf("picked window 9: the view shows %q", v.St.Window)
	}
	v.Act.PickMic("m")
	if v.St.MicID != "m" {
		t.Errorf("picked a mic: the view shows %q", v.St.MicID)
	}
	v.Act.PickCamera(home.NoCamera)
	if v.St.CameraID != home.NoCamera || len(v.St.Cameras) == 0 {
		t.Errorf("picked no camera: the view shows %q of %v", v.St.CameraID, v.St.Cameras)
	}
	on := v.St.SystemAudio
	v.Act.SetSystemAudio(!on)
	if v.St.SystemAudio == on {
		t.Errorf("turned system audio %v: the view shows %v", !on, v.St.SystemAudio)
	}
	c.set.Target, c.window = string(home.Window), 0
	v.Act.Record() // no window chosen: fails at once, starting nothing
	if v.St.Error == "" {
		t.Error("Record with no window: no error shown")
	}
}

// The camera runs while reel is in front on the home screen with a camera
// chosen and allowed, for a display or an area; and while a recording
// that shows it runs, whatever else.
func TestCamWant(t *testing.T) {
	front := camWant{chosen: true, allowed: true, active: true, home: true}
	for _, tc := range []struct {
		name string
		w    camWant
		on   bool
	}{
		{"in front, chosen and allowed", front, true},
		{"none chosen", camWant{allowed: true, active: true, home: true}, false},
		{"not allowed", camWant{chosen: true, active: true, home: true}, false},
		{"a window target", camWant{chosen: true, allowed: true, window: true, active: true, home: true}, false},
		{"another app in front", camWant{chosen: true, allowed: true, home: true}, false},
		{"the trim screen", camWant{chosen: true, allowed: true, active: true}, false},
		{"recording with it, another app in front", camWant{chosen: true, allowed: true, recording: true, recCamera: true}, true},
		{"recording without it, reel in front", camWant{chosen: true, allowed: true, active: true, home: true, recording: true}, false},
	} {
		if got := tc.w.on(); got != tc.on {
			t.Errorf("%s: on = %v, want %v", tc.name, got, tc.on)
		}
	}
}
