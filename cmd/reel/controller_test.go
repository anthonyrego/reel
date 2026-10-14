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
