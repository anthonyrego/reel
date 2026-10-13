package main

// record: a recording from the terminal, before the app has a window for
// it (M3). It records the user's screen and, with -mic, their microphone:
// the user's to run.

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/anthonyrego/reel/internal/config"
	"github.com/anthonyrego/reel/internal/logx"
	"github.com/anthonyrego/reel/internal/mac"
	"github.com/anthonyrego/reel/internal/mixer"
	"github.com/anthonyrego/reel/internal/record"
)

func init() {
	register(&command{name: "record", summary: "user only: record a display or window to an MP4 until -for or Ctrl-C", run: runRecord})
}

func runRecord(args []string) error {
	fs := flag.NewFlagSet("record", flag.ContinueOnError)
	display := fs.String("display", "", "record this display (an ID from devices, or main)")
	window := fs.Uint("window", 0, "record this window (an ID from devices)")
	app := fs.String("app", "", "record the first on-screen window of the app whose name contains this")
	mic := fs.String("mic", "", "also record this mic: an ID from devices, or default")
	systemAudio := fs.Bool("system-audio", true, "record system audio")
	dur := fs.Duration("for", 0, "stop after this long (0: at Ctrl-C)")
	fps := fs.Int("fps", 30, "the most frames a second")
	out := fs.String("out", "", "the MP4 to write (default: <recordings>/Recording <time>.mp4)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if (*display != "") == (*window != 0 || *app != "") {
		return errors.New("pick one of -display, -window or -app")
	}
	paths, err := config.Resolve()
	if err != nil {
		return err
	}
	if err := paths.Ensure(); err != nil {
		return err
	}
	logf, err := logx.Setup(paths.Logs, "record", false)
	if err != nil {
		return err
	}
	defer logf.Close()
	if *out == "" {
		*out = filepath.Join(paths.Recordings, "Recording "+time.Now().Format("2006-01-02 at 15.04.05")+".mp4")
	}
	content, err := mac.ShareableContent()
	if err != nil {
		return err
	}
	cfg := record.Config{
		Video:       mac.StreamConfig{FPS: *fps, ShowsCursor: true},
		SystemAudio: *systemAudio,
		Mic:         *mic,
		Path:        *out,
	}
	if err := pickTarget(&cfg.Video, content, *display, uint32(*window), *app); err != nil {
		return err
	}
	r, err := record.Start(cfg)
	if err != nil {
		return err
	}
	fmt.Printf("recording %d×%d to %s; Ctrl-C stops\n", cfg.Video.Width, cfg.Video.Height, *out)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	var limit <-chan time.Time
	if *dur > 0 {
		limit = time.After(*dur)
	}
	select {
	case <-sig:
	case <-limit:
	case <-r.Done():
	}
	res, err := r.Stop()
	if err != nil {
		return err
	}
	fmt.Printf("wrote %s: %.1f s, %d frames (%d dropped), audio %.1f s (%d frames lost)\n",
		res.Path, res.Duration.Seconds(), res.Frames, res.Dropped, float64(res.AudioFrames)/mixer.Rate, res.AudioLost)
	if res.StoppedBy != nil {
		fmt.Printf("the stream stopped by itself: %v\n", res.StoppedBy)
	}
	if st, err := os.Stat(res.Path); err == nil {
		fmt.Printf("%.1f MB\n", float64(st.Size())/1e6)
	}
	return nil
}
