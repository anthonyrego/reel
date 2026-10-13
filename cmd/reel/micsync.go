package main

// micsync (user only: it plays clicks and opens the mic): how late a
// microphone hears what the speakers play, against ScreenCaptureKit's tap
// of the same sound. Both are stamped on the host clock, so the gap is the
// mic's latency not accounted for in its PTS, plus the speakers' output
// latency and the air (a few ms). A Bluetooth headset's mic is the one to
// worry about: if it's far over the rest, the mixer needs an offset for it.

import (
	"errors"
	"flag"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/Zyko0/go-sdl3/sdl"

	"github.com/anthonyrego/reel/internal/avsync"
	"github.com/anthonyrego/reel/internal/config"
	"github.com/anthonyrego/reel/internal/gfx"
	"github.com/anthonyrego/reel/internal/logx"
	"github.com/anthonyrego/reel/internal/mac"
	"github.com/anthonyrego/reel/internal/mixer"
	"github.com/anthonyrego/reel/internal/platform"
)

func init() {
	register(&command{name: "micsync", summary: "user only: play clicks and measure how late the mic hears them", run: runMicsync})
}

// heard is one source's audio on the host clock: sample i was captured at
// t0 + i/rate.
type heard struct {
	mu   sync.Mutex
	t0   float64 // seconds, host clock
	rate float64
	pcm  []float32
}

func (h *heard) add(s mac.Sample, keep int) {
	pts := s.PTS().Seconds()
	s.PCM(func(f mac.ASBD, bufs [][]byte) {
		pcm, err := mixer.Decode(mixer.Format{
			Rate: f.SampleRate, Channels: int(f.ChannelsPerFrame), Bits: int(f.BitsPerChannel),
			Float: f.Float(), Interleaved: f.Interleaved(), BigEndian: f.BigEndian(),
		}, bufs, keep, nil)
		if err != nil {
			return
		}
		mono := pcm[0]
		if len(pcm) == 2 {
			for i := range mono {
				mono[i] = (pcm[0][i] + pcm[1][i]) / 2
			}
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.pcm == nil {
			h.t0, h.rate = pts, f.SampleRate
		}
		// Place by PTS: pad a gap, so sample positions stay host times.
		if at := int(math.Round((pts - h.t0) * h.rate)); at > len(h.pcm) {
			h.pcm = append(h.pcm, make([]float32, at-len(h.pcm))...)
		}
		h.pcm = append(h.pcm, mono...)
	})
}

// onsets are the click starts, as host times.
func (h *heard) onsets(threshold float64) []float64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	ts := avsync.AudioOnsets(h.pcm, h.rate, threshold, 0.2)
	for i := range ts {
		ts[i] += h.t0
	}
	return ts
}

func (h *heard) peak() float64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	var m float64
	for _, v := range h.pcm {
		m = math.Max(m, math.Abs(float64(v)))
	}
	return m
}

func runMicsync(args []string) error {
	fs := flag.NewFlagSet("micsync", flag.ContinueOnError)
	mic := fs.String("mic", mac.MicDefault, "the mic: an ID from devices, default, or none (agents: the tap alone)")
	dur := fs.Duration("for", 10*time.Second, "how long to click")
	if err := fs.Parse(args); err != nil {
		return err
	}
	paths, err := config.Resolve()
	if err != nil {
		return err
	}
	logf, err := logx.Setup(paths.Logs, "micsync", false)
	if err != nil {
		return err
	}
	defer logf.Close()
	name := ""
	if *mic != "none" {
		name = *mic
		mics, err := mac.Mics()
		if err != nil {
			return err
		}
		for _, m := range mics {
			if m.ID == *mic || (*mic == mac.MicDefault && m.Default) {
				name = fmt.Sprintf("%s (%s)", m.Name, m.Transport)
			}
		}
	}

	// ScreenCaptureKit lists only apps with a window, and the tap must hear
	// reel alone: the beacon's window, flashing on every click, is it.
	a, err := openWindow(gfx.WindowConfig{Title: "Reel mic sync", Width: 320, Height: 200, Hidden: true}, false)
	if err != nil {
		return err
	}
	defer a.close()
	if err := platform.InitAudio(); err != nil {
		return err
	}
	b := newBeacon(time.Second, *dur)
	b.gain = 0.5 // −6 dBFS: the mic hears it across the room
	defer b.close()
	a.scene = b
	a.window.Handle().SetAlwaysOnTop(true)
	a.window.Handle().Show()
	a.render()

	var tap, heardMic heard
	cfg := mac.StreamConfig{
		Target: mac.Target{OwnAppOnly: true}, Width: 2, Height: 2, FPS: 1,
		SystemAudio: true, IncludeOwnAudio: true,
	}
	if *mic != "none" {
		cfg.Mic = *mic
	}
	s, err := mac.StartStream(cfg, func(smp mac.Sample) {
		switch smp.Kind {
		case mac.KindAudio:
			tap.add(smp, 2)
		case mac.KindMic:
			heardMic.add(smp, 1)
		}
	}, nil)
	if err != nil {
		return err
	}
	if name == "" {
		fmt.Printf("clicking once a second for %s; no mic, the tap alone\n", *dur)
	} else {
		fmt.Printf("clicking once a second for %s; %s listens (speakers up, no headphones)\n", *dur, name)
	}
	if err := b.begin(); err != nil {
		s.Stop()
		return err
	}
	go func() {
		time.Sleep(*dur + 500*time.Millisecond)
		sdl.PushEvent(&sdl.Event{Type: sdl.EVENT_QUIT})
	}()
	if err := a.run(0); err != nil {
		s.Stop()
		return err
	}
	s.Stop()

	played := tap.onsets(0.05)
	fmt.Printf("the tap heard %d clicks\n", len(played))
	if len(played) == 0 {
		return errors.New("the tap heard nothing: is the output muted?")
	}
	if *mic == "none" {
		return nil
	}
	fmt.Printf("the mic peaked at %.1f dBFS\n", 20*math.Log10(math.Max(heardMic.peak(), 1e-9)))
	threshold := math.Max(heardMic.peak()/4, 0.002)
	lags := avsync.Pair(played, heardMic.onsets(threshold), 0.4)
	// Pair matches each played click with the nearest heard one; the mic
	// can only be later.
	var later []float64
	for _, l := range lags {
		if l > -0.005 {
			later = append(later, l)
		}
	}
	sum := avsync.Summarize(later)
	if sum.N < len(played)/2 {
		return fmt.Errorf("the mic heard only %d of %d clicks: turn the speakers up or move the mic closer", sum.N, len(played))
	}
	fmt.Printf("%s hears the speakers %.0f ms after the tap (p5 %.0f, p95 %.0f; %d clicks)\n",
		name, 1000*sum.Median, 1000*sum.P5, 1000*sum.P95, sum.N)
	return nil
}
