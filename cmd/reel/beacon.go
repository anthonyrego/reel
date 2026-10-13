package main

// The beacon: a window that flashes white and clicks at the same moments,
// for self-tests that record reel's own window instead of the user's
// screen. The clicks are written to the speakers in one go when it starts,
// so their timing is sample-exact; the flashes are drawn from the same
// start time.

import (
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"
	"unsafe"

	"github.com/Zyko0/go-sdl3/sdl"

	"github.com/anthonyrego/reel/internal/draw"
	"github.com/anthonyrego/reel/internal/mac"
	"github.com/anthonyrego/reel/internal/text"
	"github.com/anthonyrego/reel/internal/ui"
)

const (
	beaconRate   = 48000
	beaconFlash  = 100 * time.Millisecond // how long a flash stays white
	beaconToneHz = 1000
	beaconTone   = 20 * time.Millisecond
	beaconGain   = 0.1 // −20 dBFS: audible, not loud
)

// beacon flashes and clicks every period for the first clicksFor, then
// only flashes (with the speakers closed, so the app plays nothing).
type beacon struct {
	period    time.Duration
	clicksFor time.Duration
	gain      float64 // the clicks' peak (beaconGain unless set)

	start     time.Time
	startHost int64 // start on the host clock (ns), for fake sources
	// after, when set, runs once on the main thread a second in (logs).
	after  func()
	audio  *sdl.AudioStream
	closed bool
	tc     textCache
}

func newBeacon(period, clicksFor time.Duration) *beacon {
	return &beacon{period: period, clicksFor: clicksFor}
}

// begin opens the speakers, queues every click and starts the clock. Main
// thread, with SDL's audio initialized.
func (b *beacon) begin() error {
	if b.clicksFor <= 0 { // flashes only: the speakers stay closed
		b.start, b.startHost, b.closed = time.Now(), mac.HostTime().Nanos(), true
		slog.Info("beacon: started", "period", b.period, "clicks", false)
		return nil
	}
	spec := &sdl.AudioSpec{Format: sdl.AUDIO_F32LE, Channels: 1, Freq: beaconRate}
	s := sdl.AUDIO_DEVICE_DEFAULT_PLAYBACK.OpenAudioDeviceStream(spec, 0)
	if s == nil {
		return errors.New("beacon: couldn't open the speakers")
	}
	pcm := clicks(b.period, b.clicksFor)
	if b.gain > 0 {
		for i := range pcm {
			pcm[i] *= float32(b.gain / beaconGain)
		}
	}
	if err := s.PutData(unsafe.Slice((*byte)(unsafe.Pointer(&pcm[0])), len(pcm)*4)); err != nil {
		s.Destroy()
		return fmt.Errorf("beacon: queue the clicks: %w", err)
	}
	if err := s.ResumeDevice(); err != nil {
		s.Destroy()
		return fmt.Errorf("beacon: start the speakers: %w", err)
	}
	b.audio, b.start, b.startHost = s, time.Now(), mac.HostTime().Nanos()
	slog.Info("beacon: started", "period", b.period, "clicksFor", b.clicksFor)
	return nil
}

// clicks is mono float32 PCM: a short tone at the start of every period,
// silence between, lasting d.
func clicks(period, d time.Duration) []float32 {
	n := int(d.Seconds() * beaconRate)
	pcm := make([]float32, max(n, 1))
	tone := int(beaconTone.Seconds() * beaconRate)
	step := int(period.Seconds() * beaconRate)
	for at := 0; at < n; at += step {
		for i := 0; i < tone && at+i < n; i++ {
			// A raised-cosine envelope keeps the edges from clicking twice.
			env := 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(tone))
			pcm[at+i] = float32(beaconGain * env * math.Sin(2*math.Pi*beaconToneHz*float64(i)/beaconRate))
		}
	}
	return pcm
}

// lit reports whether the window is white at t.
func (b *beacon) lit(t time.Time) bool {
	if b.start.IsZero() {
		return false
	}
	return t.Sub(b.start)%b.period < beaconFlash
}

func (b *beacon) draw(c *ui.Ctx, l *draw.List, w, h float32) {
	// Close the speakers once the clicks are over: from then on the app
	// plays nothing at all, which is what "silence" means to
	// ScreenCaptureKit's per-app audio.
	if !b.closed && b.audio != nil && c.Now.Sub(b.start) > b.clicksFor+200*time.Millisecond {
		b.audio.Destroy()
		b.closed = true
		slog.Info("beacon: speakers closed")
	}
	if b.after != nil && !b.start.IsZero() && c.Now.Sub(b.start) > time.Second {
		b.after()
		b.after = nil
	}
	bg, fg := draw.RGB(0x101010), draw.RGB(0xf0f0f0)
	if b.lit(c.Now) {
		bg, fg = fg, bg
	}
	l.Rect(draw.Rect{W: w, H: h}, bg, draw.Radii{})
	label := "reel self-test"
	if !b.start.IsZero() {
		label = fmt.Sprintf("reel self-test  %.1fs", c.Now.Sub(b.start).Seconds())
	}
	drawAt(l, b.tc.lay(c, label, text.SansRegular, 14), 16, 14, fg)
	c.RequestFrame() // keep drawing: the flashes are on a clock
}

func (b *beacon) event(c *ui.Ctx, ev ui.Event) bool { return false }
func (b *beacon) animating() bool                   { return true }
func (b *beacon) woke()                             {}

func (b *beacon) close() {
	if b.audio != nil && !b.closed {
		b.audio.Destroy()
		b.closed = true
	}
}
