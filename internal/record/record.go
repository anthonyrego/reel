// Package record records a ScreenCaptureKit stream into an MP4.
//
// The stream's handler runs on ScreenCaptureKit's queue and only sorts
// samples: it retains a video frame that has pixels and hands it to a
// bounded channel (dropping and counting when the writer is behind). One
// goroutine, locked to its thread with an autorelease pool per step, owns
// everything else: the timeline, the AVAssetWriter and the last frame.
//
// The recording starts at zero at its first frame. ScreenCaptureKit sends
// frames only when the screen changes, so the newest frame is kept and
// written again where the picture must hold: at the end of the recording
// (so a still screen lasts until Stop) and when a pause ends (so the
// picture is the one on screen now, not the one before the pause).
//
// It calls into internal/mac but never SDL.
package record

import (
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anthonyrego/reel/internal/area"
	"github.com/anthonyrego/reel/internal/mac"
	"github.com/anthonyrego/reel/internal/mp4"
	"github.com/anthonyrego/reel/internal/timeline"
)

// Config is a recording's setup.
type Config struct {
	Stream mac.StreamConfig
	Path   string
	// Bitrate is the video's average bits a second (0: area.VideoBitrate).
	Bitrate int
	// Fragment is how often a movie fragment is written: a crash loses
	// at most that much (0: DefaultFragment; below 0: none).
	Fragment time.Duration
}

// Result is what a recording wrote.
type Result struct {
	Path     string
	Duration time.Duration // of the recording, pauses left out
	Frames   int           // video frames written (repeats included)
	// Dropped counts frames lost because the encoder or the writer was
	// behind; Skipped counts frames ScreenCaptureKit sent without new
	// pixels (idle), which carry nothing to write.
	Dropped, Skipped int
	// StoppedBy is why the stream ended by itself (nil after Stop).
	StoppedBy error
}

// frame is a retained video sample on its way to the writer.
type frame struct {
	sbuf uintptr
	pts  int64 // host ns
}

type ctlKind int

const (
	ctlStop ctlKind = iota
	ctlPause
	ctlResume
)

type ctl struct {
	kind ctlKind
	at   int64 // host ns
	done chan struct{}
}

// Recorder is a recording in progress.
type Recorder struct {
	cfg    Config
	fps    int
	stream *mac.Stream
	writer *mac.Writer

	video chan frame
	ctl   chan ctl

	skipped, queueDrops atomic.Int64

	stopOnce sync.Once
	finished chan struct{} // closed when the writer goroutine is done
	result   Result
	err      error
	stopped  atomic.Pointer[error] // the stream ended by itself
}

// DefaultFragment is how often a recording writes a movie fragment.
// Killed mid-recording at 12 s with fragments every 5 s, 10 s of it could
// still be read; without fragments the file is empty.
const DefaultFragment = 5 * time.Second

// videoQueue is how many frames may wait for the writer. Each holds one of
// ScreenCaptureKit's surfaces (queueDepth 8, of which the writer keeps one
// and the encoder a few).
const videoQueue = 3

// Start opens the file and starts recording.
func Start(cfg Config) (*Recorder, error) {
	switch {
	case cfg.Fragment == 0:
		cfg.Fragment = DefaultFragment
	case cfg.Fragment < 0:
		cfg.Fragment = 0
	}
	fps := cfg.Stream.FPS
	if fps <= 0 {
		fps = 30
		cfg.Stream.FPS = fps
	}
	bitrate := cfg.Bitrate
	if bitrate <= 0 {
		bitrate = area.VideoBitrate(cfg.Stream.Width, cfg.Stream.Height, fps)
	}
	w, err := mac.NewWriter(cfg.Path, mac.VideoSettings{
		Width: cfg.Stream.Width, Height: cfg.Stream.Height, FPS: fps, Bitrate: bitrate,
	}, cfg.Fragment)
	if err != nil {
		return nil, err
	}
	r := &Recorder{
		cfg: cfg, fps: fps, writer: w,
		video:    make(chan frame, videoQueue),
		ctl:      make(chan ctl),
		finished: make(chan struct{}),
	}
	go r.run()
	r.stream, err = mac.StartStream(cfg.Stream, r.sample, r.streamStopped)
	if err != nil {
		done := make(chan struct{})
		r.ctl <- ctl{kind: ctlStop, at: -1, done: done} // -1: cancel
		<-done
		return nil, err
	}
	slog.Info("record: started", "path", cfg.Path, "size", fmt.Sprintf("%d×%d", cfg.Stream.Width, cfg.Stream.Height),
		"fps", fps, "bitrate", bitrate, "fragment", cfg.Fragment)
	return r, nil
}

// sample runs on ScreenCaptureKit's queue: sort, retain, hand over.
func (r *Recorder) sample(s mac.Sample) {
	if s.Kind != mac.KindScreen {
		return
	}
	st, ok := s.FrameStatus()
	if !ok || (st != mac.FrameComplete && st != mac.FrameStarted) {
		r.skipped.Add(1)
		return
	}
	if _, _, _, ok := s.Image(); !ok {
		r.skipped.Add(1)
		return
	}
	pts := s.PTS()
	if !pts.Valid() {
		r.skipped.Add(1)
		return
	}
	f := frame{sbuf: mac.Retain(s.Ref()), pts: pts.Nanos()}
	select {
	case r.video <- f:
	default:
		mac.Release(f.sbuf)
		r.queueDrops.Add(1)
	}
}

// streamStopped hears the stream end by itself (the menu bar's stop
// control, sleep, the window closed): the recording ends there.
func (r *Recorder) streamStopped(err error) {
	if err == nil {
		return // Stop's own
	}
	r.stopped.Store(&err)
	go r.Stop()
}

// Pause leaves out what's captured from now until Resume.
func (r *Recorder) Pause() { r.send(ctlPause) }

// Resume records again.
func (r *Recorder) Resume() { r.send(ctlResume) }

func (r *Recorder) send(k ctlKind) {
	done := make(chan struct{})
	select {
	case r.ctl <- ctl{kind: k, at: mac.HostTime().Nanos(), done: done}:
		<-done
	case <-r.finished:
	}
}

// Stop ends the recording now and finishes the file. It's safe to call
// more than once, and from any goroutine; every call returns the same
// result.
func (r *Recorder) Stop() (Result, error) {
	r.stopOnce.Do(func() {
		at := mac.HostTime().Nanos()
		if r.stopped.Load() == nil {
			if err := r.stream.Stop(); err != nil {
				slog.Warn("record: stop the stream", "err", err)
			}
		}
		done := make(chan struct{})
		r.ctl <- ctl{kind: ctlStop, at: at, done: done}
		<-done
	})
	<-r.finished
	return r.result, r.err
}

// Done is closed when the recording has ended (by Stop or by itself).
func (r *Recorder) Done() <-chan struct{} { return r.finished }

// run is the writer goroutine.
func (r *Recorder) run() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	w := writing{r: r, frameDur: int64(time.Second) / int64(r.fps)}
	defer close(r.finished)
	for {
		select {
		case f := <-r.video:
			mac.WithPool(func() { w.frame(f) })
		case c := <-r.ctl:
			switch c.kind {
			case ctlPause:
				w.tl.Pause(c.at)
			case ctlResume:
				mac.WithPool(func() { w.resume(c.at) })
			case ctlStop:
				mac.WithPool(func() { w.stop(c.at) })
				close(c.done)
				return
			}
			close(c.done)
		}
	}
}

// writing is the writer goroutine's state.
type writing struct {
	r        *Recorder
	tl       timeline.Timeline
	frameDur int64

	last    frame // the newest frame with pixels, retained (sbuf 0: none)
	lastOut int64 // the recording time of the last frame written
	wrote   bool
	err     error
}

// frame writes one frame at its place in the recording.
func (w *writing) frame(f frame) {
	if !w.tl.Started() {
		w.tl.Start(f.pts)
	}
	// Whatever happens to it, it's now the newest picture.
	if w.last.sbuf != 0 {
		mac.Release(w.last.sbuf)
	}
	w.last = f
	out, keep := w.tl.Map(f.pts)
	if !keep || w.err != nil {
		return
	}
	w.write(f.sbuf, out, mac.CMTime{})
}

// write appends a frame at recording time out (ns) lasting dur (zero:
// until the next one). Times must only go forward.
func (w *writing) write(sbuf uintptr, out int64, dur mac.CMTime) {
	if w.wrote && out <= w.lastOut {
		return
	}
	ok, err := w.r.writer.AppendVideo(sbuf, mac.Nanos(out), dur)
	if err != nil {
		slog.Error("record: write a frame", "err", err)
		w.err = err
		return
	}
	if ok {
		w.wrote, w.lastOut = true, out
	}
}

// resume ends the pause and writes the newest picture again where the
// recording picks up.
func (w *writing) resume(at int64) {
	w.tl.Resume(at)
	if w.last.sbuf == 0 || w.err != nil {
		return
	}
	w.write(w.last.sbuf, w.tl.At(at), mac.CMTime{})
}

// stop finishes the file at host time at (-1: cancel it).
func (w *writing) stop(at int64) {
	r := w.r
	defer func() {
		if w.last.sbuf != 0 {
			mac.Release(w.last.sbuf)
			w.last.sbuf = 0
		}
	}()
	// Frames already captured before the stop still belong in it.
	for drained := false; !drained; {
		select {
		case f := <-r.video:
			if at >= 0 && f.pts <= at {
				w.frame(f)
			} else {
				mac.Release(f.sbuf)
			}
		default:
			drained = true
		}
	}
	res := Result{Path: r.cfg.Path, Skipped: int(r.skipped.Load()), Frames: r.writer.Appended}
	if p := r.stopped.Load(); p != nil {
		res.StoppedBy = *p
	}
	switch {
	case at < 0:
		r.writer.Cancel()
		r.err = errors.New("record: cancelled")
		return
	case !w.wrote:
		r.writer.Cancel()
		r.err = errors.New("record: no frames were captured (is the window covered, or the display asleep?)")
		return
	case w.err != nil:
		r.writer.Cancel()
		r.err = w.err
		return
	}
	end := w.tl.At(at)
	// The picture holds until the end: write the newest frame once more
	// just before it, lasting one frame.
	if hold := end - w.frameDur; hold > w.lastOut && w.last.sbuf != 0 {
		for i := 0; i < 50; i++ { // the encoder may be busy for a moment
			before := r.writer.Appended
			w.write(w.last.sbuf, hold, mac.Nanos(w.frameDur))
			if r.writer.Appended > before || w.err != nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	} else {
		end = max(end, w.lastOut+w.frameDur)
	}
	if err := r.writer.Finish(mac.Nanos(end)); err != nil {
		r.err = err
		return
	}
	if r.cfg.Fragment > 0 {
		// AVAssetWriter puts a fragmented recording's final moov last.
		if err := mp4.FastStart(r.cfg.Path); err != nil {
			r.err = fmt.Errorf("record: move the index to the front: %w", err)
			return
		}
	}
	res.Frames = r.writer.Appended
	res.Dropped = r.writer.Dropped + int(r.queueDrops.Load())
	res.Duration = time.Duration(end)
	r.result = res
	slog.Info("record: finished", "path", res.Path, "duration", res.Duration, "frames", res.Frames,
		"dropped", res.Dropped, "skipped", res.Skipped)
}
