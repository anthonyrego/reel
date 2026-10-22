package main

// Sample states of the main window, for renders and goldens.

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/anthonyrego/reel/internal/ui/controls"
	"github.com/anthonyrego/reel/internal/ui/home"
)

// samples are the main window's states by name.
var samples = map[string]func(now time.Time) (home.State, string){
	"idle": func(now time.Time) (home.State, string) { return sampleIdle(now), "" },
	"live": func(now time.Time) (home.State, string) {
		s := sampleIdle(now)
		s.Live = &home.Live{Elapsed: 83 * time.Second}
		return s, ""
	},
	"paused": func(now time.Time) (home.State, string) {
		s := sampleIdle(now)
		s.Live = &home.Live{Elapsed: 83 * time.Second, Paused: true}
		return s, ""
	},
	"mic-menu":    func(now time.Time) (home.State, string) { return sampleIdle(now), "mic" },
	"camera-menu": func(now time.Time) (home.State, string) { return sampleIdle(now), "camera" },
	"no-camera": func(now time.Time) (home.State, string) {
		s := sampleIdle(now)
		s.CameraID = home.NoCamera
		return s, ""
	},
	"window": func(now time.Time) (home.State, string) {
		s := sampleIdle(now)
		s.Target, s.Window = home.Window, "41"
		return s, "window"
	},
	"window-camera": func(now time.Time) (home.State, string) {
		s := sampleIdle(now)
		s.Target, s.Window = home.Window, "41"
		return s, ""
	},
	"area": func(now time.Time) (home.State, string) {
		s := sampleIdle(now)
		s.Target, s.AreaLabel = home.Area, "1280 × 720 at 96, 140 on the Studio Display"
		return s, ""
	},
	"first-run": func(now time.Time) (home.State, string) {
		s := sampleIdle(now)
		s.Screen, s.Mic, s.Recordings = home.PermAsk, home.PermDenied, nil
		return s, ""
	},
	"hover-card": func(now time.Time) (home.State, string) { return sampleIdle(now), "" },
	"hover-play": func(now time.Time) (home.State, string) { return sampleIdle(now), "" },
	"delete":     func(now time.Time) (home.State, string) { return sampleIdle(now), "" },
	"empty": func(now time.Time) (home.State, string) {
		s := sampleIdle(now)
		s.Recordings = nil
		return s, ""
	},
	"error": func(now time.Time) (home.State, string) {
		s := sampleIdle(now)
		s.MicID = "BuiltInMicrophoneDevice"
		s.Warning = "On the speakers, the mic hears the system audio too: wear headphones."
		s.Error = "The recording stopped by itself (the display was disconnected); what it got is saved."
		return s, ""
	},
	"starting": func(now time.Time) (home.State, string) {
		s := sampleIdle(now)
		s.Starting = true
		return s, ""
	},
	"finishing": func(now time.Time) (home.State, string) {
		s := sampleIdle(now)
		s.Finishing = true
		return s, ""
	},
	"copied": func(now time.Time) (home.State, string) {
		s := sampleIdle(now)
		s.Copied, s.Toast, s.ToastAt = s.Recordings[0].Path, "Copied: paste it into Slack", now.Add(-time.Second)
		return s, ""
	},
}

// sampleNames lists the samples.
func sampleNames() string {
	var ns []string
	for n := range samples {
		ns = append(ns, n)
	}
	sort.Strings(ns)
	return strings.Join(ns, ", ")
}

func sampleIdle(now time.Time) home.State {
	recs := []home.Recording{}
	for i, d := range []time.Duration{62 * time.Second, 4*time.Minute + 11*time.Second, 38 * time.Second, 12*time.Minute + 5*time.Second, 95 * time.Second} {
		when := now.Add(-time.Duration(i*i+1) * 7 * time.Hour)
		recs = append(recs, home.Recording{
			Path: fmt.Sprintf("/rec/%d.mp4", i), Name: "Recording " + when.Format("2006-01-02 at 15.04.05"),
			Duration: d, Size: int64(d.Seconds() * 400_000), When: when,
		})
	}
	recs[1].Name += " trimmed"
	recs[3].Name = "Onboarding walkthrough for the team"
	return home.State{
		Target:   home.Display,
		Displays: []controls.Choice{{ID: "2", Label: "Studio Display", Detail: "3008 × 1692"}, {ID: "1", Label: "Built-in Retina Display", Detail: "1728 × 1117"}},
		Display:  "2",
		Windows: []controls.Choice{
			{ID: "40", Label: "Google Chrome", Detail: "Pull request #42 · reel"},
			{ID: "41", Label: "Ghostty", Detail: "~/projects/reel"},
			{ID: "42", Label: "Slack", Detail: "#eng"},
			{ID: "43", Label: "Figma", Detail: "Onboarding flow v3"},
		},
		Mics: []controls.Choice{
			{ID: home.NoMic, Label: "No microphone"},
			{ID: "usb-interface", Label: "USB Audio Interface", Detail: "default"},
			{ID: "BuiltInMicrophoneDevice", Label: "MacBook Pro Microphone"},
			{ID: "airpods", Label: "AirPods Pro", Detail: "Bluetooth"},
		},
		MicID: "usb-interface",
		Cameras: []controls.Choice{
			{ID: home.NoCamera, Label: "No camera"},
			{ID: "facetime", Label: "FaceTime HD Camera"},
			{ID: "brio", Label: "Logitech BRIO"},
			{ID: "iphone", Label: "iPhone Camera", Detail: "iPhone"},
		},
		CameraID:    "facetime",
		SystemAudio: true,
		HotKey:      "⌃⌥R",
		Recordings:  recs,
	}
}

// sampleActions does nothing, so renders draw every control enabled.
func sampleActions() home.Actions {
	nop, nop1 := func() {}, func(string) {}
	return home.Actions{
		AskScreen: nop, AskMic: nop, SetTarget: func(home.Target) {}, PickDisplay: nop1, PickWindow: nop1,
		SelectArea: nop, PickMic: nop1, PickCamera: nop1, SetSystemAudio: func(bool) {}, Record: nop, Pause: nop, Resume: nop,
		Stop: nop, Copy: nop1, Reveal: nop1, Open: nop1, Delete: nop1, Trim: nop1, Play: nop1, OpenFolder: nop,
	}
}
