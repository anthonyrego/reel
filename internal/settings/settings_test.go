package settings

import (
	"path/filepath"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s", "settings.json")
	s, err := Load(path)
	if err != nil || s != Defaults() {
		t.Fatalf("missing file: %+v, %v", s, err)
	}
	s.Target, s.Mic, s.MicChosen, s.SystemAudio = "area", "usb-interface", true, false
	s.Area = &Area{Display: 2, X: 10, Y: 20, W: 1280, H: 720}
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Target != "area" || got.Mic != "usb-interface" || got.SystemAudio || got.Area == nil || *got.Area != *s.Area {
		t.Errorf("got %+v", got)
	}
}
