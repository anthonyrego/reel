package main

// selftest: records the beacon (reel's own window, flashing and clicking)
// into an MP4 the way the app records, then reads the file back and checks
// it. It never records the user's screen or opens a mic, so agents run it.
//
// The beacon's clicks come back as system audio through the second,
// audio-only stream a window recording uses (hearing only reel). A fake
// microphone can join them: clicks a quarter period after each flash, at
// 44.1 kHz from a clock running -mic-ppm fast, fed in as a mic would be,
// so resampling and drift correction run without a device.

import (
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Zyko0/go-sdl3/sdl"

	"github.com/anthonyrego/reel/internal/area"
	"github.com/anthonyrego/reel/internal/config"
	"github.com/anthonyrego/reel/internal/gfx"
	"github.com/anthonyrego/reel/internal/logx"
	"github.com/anthonyrego/reel/internal/mac"
	"github.com/anthonyrego/reel/internal/mixer"
	"github.com/anthonyrego/reel/internal/platform"
	"github.com/anthonyrego/reel/internal/record"
)

func init() {
	register(&command{name: "selftest", summary: "record reel's own beacon window to an MP4 and check it (agents: safe)", run: runSelftest})
}

const (
	beaconPeriod = 500 * time.Millisecond
	// fakeMicAfter is where the fake mic's clicks fall after each flash.
	fakeMicAfter = 250 * time.Millisecond
	fakeMicRate  = 44100
	fakeMicHz    = 500
)

func runSelftest(args []string) error {
	fs := flag.NewFlagSet("selftest", flag.ContinueOnError)
	dur := fs.Duration("for", 20*time.Second, "how long to record")
	videoOnly := fs.Bool("video-only", false, "record no audio (M2)")
	fakeMic := fs.Bool("fake-mic", true, "feed a fake 44.1 kHz microphone between the clicks")
	micPPM := fs.Float64("mic-ppm", 800, "how fast the fake mic's clock runs, in ppm")
	fragment := fs.Duration("fragment", 0, "write a movie fragment this often (0: the default, 5s; -1s: none)")
	fps := fs.Int("fps", 30, "the most frames a second")
	out := fs.String("out", "", "the MP4 to write (default: <recordings>/selftest-<time>.mp4)")
	pauseAt := fs.Duration("pause-at", 0, "pause this far in (0: never); the beacon turns still and blue a second into the pause")
	pauseFor := fs.Duration("pause-for", 3*time.Second, "with -pause-at: how long to pause")
	hideAt := fs.Duration("hide-at", 0, "hide the recorded window this far in (0: never): the recording pauses until it shows")
	interruptAt := fs.Duration("interrupt-at", 0, "end the recording this far in as if the stream had stopped by itself (0: never)")
	minimize := fs.Bool("minimize", false, "with -hide-at: minimize the window instead of hiding it")
	showAt := fs.Duration("show-at", 0, "with -hide-at: show the window again this far in (0: never)")
	trimStart := fs.Duration("trim-from", 0, "with -trim-to: trim the recording from here, as the app does, and check the copy")
	trimEnd := fs.Duration("trim-to", 0, "trim the recording to here (0: don't trim)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *trimEnd > 0 && (*trimEnd <= *trimStart || *trimEnd > *dur || *pauseAt > 0) {
		return errors.New("-trim-to must come after -trim-from, within -for, without -pause-at")
	}
	paths, err := config.Resolve()
	if err != nil {
		return err
	}
	if err := paths.Ensure(); err != nil {
		return err
	}
	logf, err := logx.Setup(paths.Logs, "selftest", false)
	if err != nil {
		return err
	}
	defer logf.Close()
	if *out == "" {
		// Beside the recordings, not among them: the app lists those.
		dir := filepath.Join(paths.Data, "selftests")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		*out = filepath.Join(dir, "selftest-"+time.Now().Format("20060102-150405")+".mp4")
	}

	a, err := openWindow(gfx.WindowConfig{Title: "Reel self-test", Width: 480, Height: 300, Hidden: true}, false)
	if err != nil {
		return err
	}
	defer a.close()
	clicksFor := time.Duration(0)
	if !*videoOnly {
		if err := platform.InitAudio(); err != nil {
			return err
		}
		clicksFor = *dur + 2*time.Second
	}
	b := newBeacon(beaconPeriod, clicksFor)
	defer b.close()
	a.setScene(b)
	// A covered window isn't drawn: keep the beacon above everything.
	a.window.Handle().SetAlwaysOnTop(true)
	a.window.Handle().Show()
	a.window.Handle().Raise()
	a.render(a.main)
	id, err := platform.WindowNumber(a.window.Handle())
	if err != nil {
		return err
	}
	// The window as ScreenCaptureKit records it: title bar and all.
	ww, wh, err := platform.WindowFrameSize(a.window.Handle())
	if err != nil {
		return err
	}
	pw, ph := area.Pixels(ww, wh, float64(a.window.Scale()), area.MaxW, area.MaxH)
	cfg := record.Config{
		Video: mac.StreamConfig{
			Target: mac.Target{WindowID: id}, Width: pw, Height: ph, FPS: *fps, ShowsCursor: false,
		},
		SystemAudio:  !*videoOnly,
		OwnAudioOnly: true,
		Path:         *out,
		Fragment:     *fragment,
	}
	if err := b.begin(); err != nil {
		return err
	}
	refresh := displayRefresh(a.window.Handle())
	var hide, show atomic.Bool
	b.onWake = func() {
		if hide.Swap(false) {
			if *minimize {
				a.window.Handle().Minimize()
			} else {
				a.window.Handle().Hide()
			}
		}
		if show.Swap(false) {
			a.window.Handle().Restore()
			a.window.Handle().Show()
			a.window.Handle().Raise()
		}
	}

	type outcome struct {
		res record.Result
		err error
	}
	got := make(chan outcome, 1)
	go func() {
		defer sdl.PushEvent(&sdl.Event{Type: sdl.EVENT_QUIT})
		r, err := record.Start(cfg)
		if err != nil {
			got <- outcome{err: err}
			return
		}
		if *pauseAt > 0 {
			go pauseBeacon(r, b, *pauseAt, *pauseFor)
		}
		if *interruptAt > 0 {
			go func() {
				time.Sleep(*interruptAt)
				r.Interrupt(errors.New("the stream stopped (simulated)"))
			}()
		}
		if *hideAt > 0 {
			go func() {
				time.Sleep(*hideAt)
				hide.Store(true)
				platform.Wake()
				if *showAt > *hideAt {
					time.Sleep(*showAt - *hideAt)
					show.Store(true)
					platform.Wake()
				}
			}()
		}
		stopMic := func() {}
		if *fakeMic && !*videoOnly {
			stopMic = feedFakeMic(r, b.startHost, *micPPM)
		}
		select {
		case <-time.After(*dur):
		case <-r.Done():
		}
		stopMic()
		res, err := r.Stop()
		got <- outcome{res, err}
	}()
	if err := a.run(0); err != nil {
		return err
	}
	o := <-got
	if o.err != nil {
		return o.err
	}
	if o.res.StoppedBy != nil {
		fmt.Printf("the stream ended by itself: %v\n", o.res.StoppedBy)
	}
	fmt.Printf("wrote %s: %.3f s, %d frames, %d dropped, %d skipped; audio %.3f s, %d frames lost\n",
		o.res.Path, o.res.Duration.Seconds(), o.res.Frames, o.res.Dropped, o.res.Skipped,
		float64(o.res.AudioFrames)/mixer.Rate, o.res.AudioLost)
	if st, err := os.Stat(o.res.Path); err == nil {
		fmt.Printf("      %.1f MB; the display refreshes at %.0f Hz\n", float64(st.Size())/1e6, 1/refresh)
	}
	micAfter := 0.0
	if *fakeMic && !*videoOnly {
		micAfter = fakeMicAfter.Seconds()
	}
	c := newChecks(os.Stdout)
	c.check(o.res.Dropped == 0, "nothing dropped", "%d frames dropped", o.res.Dropped)
	if !*videoOnly {
		c.check(o.res.AudioLost == 0, "no audio lost", "%d frames lost", o.res.AudioLost)
	}
	if *pauseAt > 0 {
		analyzePause(c, o.res, *pauseFor, beaconPeriod.Seconds(), 1/float64(*fps))
		return c.result()
	}
	want := expect{
		duration: o.res.Duration.Seconds(), frameDur: 1 / float64(*fps), period: beaconPeriod.Seconds(),
		videoOnly: *videoOnly, refresh: refresh,
		micAfter: micAfter,
	}
	analyzeFile(c, o.res.Path, want)
	if *trimEnd > 0 {
		trim := strings.TrimSuffix(o.res.Path, ".mp4") + "-trim.mp4"
		start := time.Now()
		if err := trimTo(o.res.Path, trim, *trimStart, *trimEnd); err != nil {
			c.check(false, "trim", "%v", err)
			return c.result()
		}
		fmt.Printf("trimmed %.3f to %.3f s into %s in %v\n", trimStart.Seconds(), trimEnd.Seconds(), trim,
			time.Since(start).Round(time.Millisecond))
		analyzeTrim(c, o.res.Path, trim, *trimStart, *trimEnd, want)
	}
	return c.result()
}

// displayRefresh is the seconds between refreshes of the window's display
// (a flash shows at the first refresh after it's due, so its onset in the
// video wobbles by up to this much).
func displayRefresh(w *sdl.Window) float64 {
	if mode, err := sdl.GetDisplayForWindow(w).CurrentDisplayMode(); err == nil && mode.RefreshRate > 0 {
		return 1 / float64(mode.RefreshRate)
	}
	return 1.0 / 60
}

// feedFakeMic plays a fake microphone into the recording until the
// returned func is called: 10 ms buffers of 44.1 kHz audio, stamped with
// the host time they'd be captured at by a device whose clock runs ppm
// fast (so its 44 100 samples a "second" take less than a second), with a
// 500 Hz click fakeMicAfter past every flash.
func feedFakeMic(r *record.Recorder, startHost int64, ppm float64) (stop func()) {
	trueRate := fakeMicRate * (1 + ppm*1e-6)
	const chunk = fakeMicRate / 100
	tone := int(beaconTone.Seconds() * fakeMicRate)
	// click reports the click sample at device sample n, if any.
	click := func(n int64) float32 {
		t := float64(n) / trueRate // true seconds since the beacon started
		k := math.Floor((t - fakeMicAfter.Seconds()) / beaconPeriod.Seconds())
		at := (k*beaconPeriod.Seconds() + fakeMicAfter.Seconds()) * trueRate // the click's first sample
		i := float64(n) - math.Ceil(at)
		if k < 0 || i < 0 || i >= float64(tone) {
			return 0
		}
		env := 0.5 - 0.5*math.Cos(2*math.Pi*i/float64(tone))
		return float32(beaconGain * env * math.Sin(2*math.Pi*fakeMicHz*i/fakeMicRate))
	}
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		// Start from now, as a device opened now would.
		n := int64(float64(mac.HostTime().Nanos()-startHost) / 1e9 * trueRate)
		t := time.NewTicker(10 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
			}
			// Feed every buffer whose last sample has been "captured".
			for {
				end := startHost + int64(float64(n+chunk)/trueRate*1e9)
				if end > mac.HostTime().Nanos() {
					break
				}
				pcm := make([]float32, chunk)
				for i := range pcm {
					pcm[i] = click(n + int64(i))
				}
				r.Feed(mixer.Mic, startHost+int64(float64(n)/trueRate*1e9), fakeMicRate, [][]float32{pcm})
				n += chunk
			}
		}
	}()
	return func() { close(done); <-finished }
}

// pauseBeacon pauses the recording at pauseAt for pauseFor. A second into
// the pause the beacon turns still and blue, and stays so for two seconds
// after the recording resumes: the screen changed during the pause and is
// still after it, so only the frame written again at the resume can show
// the blue. Then it flashes again.
func pauseBeacon(r *record.Recorder, b *beacon, pauseAt, pauseFor time.Duration) {
	time.Sleep(pauseAt)
	r.Pause()
	time.Sleep(time.Second)
	b.frozen.Store(true)
	platform.Wake() // draw the blue once
	time.Sleep(pauseFor - time.Second)
	r.Resume()
	time.Sleep(pauseStill)
	b.frozen.Store(false)
	platform.Wake()
}

// pauseStill is how long the beacon stays still after a resume.
const pauseStill = 2 * time.Second
