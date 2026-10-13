package main

// selftest: records the beacon (reel's own window, flashing and clicking)
// into an MP4 the way the app records, then reads the file back and checks
// it. It never records the user's screen or opens a mic, so agents run it.

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Zyko0/go-sdl3/sdl"

	"github.com/anthonyrego/reel/internal/area"
	"github.com/anthonyrego/reel/internal/config"
	"github.com/anthonyrego/reel/internal/gfx"
	"github.com/anthonyrego/reel/internal/logx"
	"github.com/anthonyrego/reel/internal/mac"
	"github.com/anthonyrego/reel/internal/platform"
	"github.com/anthonyrego/reel/internal/record"
)

func init() {
	register(&command{name: "selftest", summary: "record reel's own beacon window to an MP4 and check it (agents: safe)", run: runSelftest})
}

const beaconPeriod = 500 * time.Millisecond

func runSelftest(args []string) error {
	fs := flag.NewFlagSet("selftest", flag.ContinueOnError)
	dur := fs.Duration("for", 8*time.Second, "how long to record")
	videoOnly := fs.Bool("video-only", false, "record no audio (M2)")
	fragment := fs.Duration("fragment", 0, "write a movie fragment this often (0: the default, 5s; -1s: none)")
	fps := fs.Int("fps", 30, "the most frames a second")
	out := fs.String("out", "", "the MP4 to write (default: <recordings>/selftest-<time>.mp4)")
	if err := fs.Parse(args); err != nil {
		return err
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
		*out = filepath.Join(paths.Recordings, "selftest-"+time.Now().Format("20060102-150405")+".mp4")
	}
	if !*videoOnly {
		return errors.New("audio arrives in M3: run with -video-only")
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
		clicksFor = *dur + time.Second
	}
	b := newBeacon(beaconPeriod, clicksFor)
	defer b.close()
	a.scene = b
	// A covered window isn't drawn: keep the beacon above everything.
	a.window.Handle().SetAlwaysOnTop(true)
	a.window.Handle().Show()
	a.window.Handle().Raise()
	a.render()
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
		Stream: mac.StreamConfig{
			Target: mac.Target{WindowID: id}, Width: pw, Height: ph, FPS: *fps, ShowsCursor: false,
		},
		Path:     *out,
		Fragment: *fragment,
	}
	if err := b.begin(); err != nil {
		return err
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
		select {
		case <-time.After(*dur):
		case <-r.Done():
		}
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
	fmt.Printf("wrote %s: %.3f s, %d frames, %d dropped, %d skipped\n",
		o.res.Path, o.res.Duration.Seconds(), o.res.Frames, o.res.Dropped, o.res.Skipped)
	if st, err := os.Stat(o.res.Path); err == nil {
		fmt.Printf("      %.1f MB\n", float64(st.Size())/1e6)
	}
	c := newChecks(os.Stdout)
	c.check(o.res.Dropped == 0, "nothing dropped", "%d dropped", o.res.Dropped)
	analyzeFile(c, o.res.Path, expect{
		duration: o.res.Duration.Seconds(), frameDur: 1 / float64(*fps), period: beaconPeriod.Seconds(), videoOnly: *videoOnly,
	})
	return c.result()
}
