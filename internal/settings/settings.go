// Package settings keeps what the user chose between runs: what to
// record, the microphone, system audio, the last area, the camera and
// where its bubble sits. Pure.
package settings

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// Settings are the user's choices.
type Settings struct {
	Target      string `json:"target"`  // "display", "window" or "area"
	Display     uint32 `json:"display"` // CGDirectDisplayID; 0: the main one
	Mic         string `json:"mic"`     // AVCaptureDevice uniqueID; "" for none
	MicChosen   bool   `json:"micChosen"`
	SystemAudio bool   `json:"systemAudio"`
	Area        *Area  `json:"area,omitempty"`
	FPS         int    `json:"fps"`
	// Camera is the bubble's camera (AVCaptureDevice uniqueID); "" for
	// none, a new user's choice.
	Camera string  `json:"camera,omitempty"`
	Bubble *Bubble `json:"bubble,omitempty"`
}

// Bubble is where the camera bubble sits: its circle's top-left corner
// and diameter, in global points (y down from the main display's top).
type Bubble struct {
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	Size float64 `json:"size"`
}

// Area is a part of a display, in the display's own points.
type Area struct {
	Display uint32  `json:"display"`
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	W       float64 `json:"w"`
	H       float64 `json:"h"`
}

// Defaults are a new user's: the main display, system audio on, the
// system's default mic (chosen when the mics are first listed).
func Defaults() Settings {
	return Settings{Target: "display", SystemAudio: true, FPS: 30}
}

// Load reads the settings at path; a missing file is the defaults.
func Load(path string) (Settings, error) {
	s := Defaults()
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return Defaults(), err
	}
	if s.FPS <= 0 {
		s.FPS = 30
	}
	return s, nil
}

// Save writes the settings beside path and renames them into place.
func (s Settings) Save(path string) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
