package settings

import (
	"os"
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
	s.Camera, s.Bubble = "camera-uid", &Bubble{X: -400, Y: 900, Size: 260, Zoom: 1.6, PanX: 0.12, PanY: -0.05, Round: 0.3}
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Target != "area" || got.Mic != "usb-interface" || got.SystemAudio || got.Area == nil || *got.Area != *s.Area ||
		got.Camera != "camera-uid" || got.Bubble == nil || *got.Bubble != *s.Bubble {
		t.Errorf("got %+v", got)
	}
}

// A bubble saved before it had a look reads as a circle showing the
// middle square; a square saved since stays one.
func TestBubbleLookDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"target":"display","camera":"c","bubble":{"x":10,"y":20,"size":220}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := (Bubble{X: 10, Y: 20, Size: 220, Zoom: 1, Round: 1}); s.Bubble == nil || *s.Bubble != want {
		t.Errorf("an older bubble: %+v, want %+v", s.Bubble, want)
	}
	s.Bubble.Round = 0
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	if got, err := Load(path); err != nil || got.Bubble.Round != 0 {
		t.Errorf("a square: round %v, %v", got.Bubble.Round, err)
	}
}
