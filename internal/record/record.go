// Package record records the screen, system audio and a microphone into
// an MP4 with one video track and one mixed audio track.
//
// ScreenCaptureKit's handlers run on its queue and only sort samples: a
// video frame with pixels is retained, audio is decoded to float32, and
// each goes to a bounded channel (dropped and counted when the writer is
// behind). One goroutine, locked to its thread with an autorelease pool
// per step, owns everything else: the timeline, the mixer, the
// AVAssetWriter and the newest frame.
//
// The recording starts at zero at its first frame. Audio from before it
// is dropped; after it, each buffer goes to the mixer at the place its PTS
// maps to. A 20 ms tick emits the mix up to jitterWindow behind now, since
// audio arrives up to about 80 ms after its PTS. ScreenCaptureKit sends
// frames only when the screen changes, so the newest frame is written
// again where the picture must hold: at the end (a still screen lasts
// until Stop) and when a pause ends (the picture is the one on screen
// now).
//
// A display (or an area of one) is one stream. A window is two: a window
// filter hears only the window's own app, so its system audio and the mic
// come from a second, audio-only stream of the display.
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
	"github.com/anthonyrego/reel/internal/mixer"
	"github.com/anthonyrego/reel/internal/mp4"
	"github.com/anthonyrego/reel/internal/timeline"
)

// Config is a recording's setup.
type Config struct {
	// Video is what to record and at what size. Its SystemAudio and Mic
	// are ignored: say them here instead.
	Video mac.StreamConfig
	// SystemAudio records what the Mac plays (other apps; not reel).
	SystemAudio bool
	// Mic is the microphone: an AVCaptureDevice uniqueID, mac.MicDefault,
	// or "" for none.
	Mic string
	// OwnAudioOnly hears only reel itself (self-tests): its sound comes
	// through the same second stream a window recording uses.
	OwnAudioOnly bool

	Path string
	// Bitrate is the video's average bits a second (0: area.VideoBitrate).
	Bitrate int
	// Fragment is how often a movie fragment is written: a crash loses
	// at most that much (0: DefaultFragment; below 0: none).
	Fragment time.Duration
}

// audio reports whether the recording has an audio track.
func (c Config) audio() bool { return c.SystemAudio || c.Mic != "" }

// Result is what a recording wrote.
type Result struct {
	Path     string
	Duration time.Duration // of the recording, pauses left out
	Frames   int           // video frames written (repeats included)
	// Dropped counts frames lost because the encoder or the writer was
	// behind; Skipped counts frames ScreenCaptureKit sent without new
	// pixels (idle), which carry nothing to write.
	Dropped, Skipped int
	// AudioFrames were written; AudioLost were captured but lost: queued
	// too long, or arrived after their place was written.
	AudioFrames, AudioLost int64
	// StoppedBy is why the stream ended by itself (nil after Stop).
	StoppedBy error
}

// DefaultFragment is how often a recording writes a movie fragment.
// Killed mid-recording at 12 s with fragments every 5 s, 10 s of it could
// still be read; without fragments the file is empty.
const DefaultFragment = 5 * time.Second

const (
	// videoQueue is how many frames may wait for the writer. Each holds
	// one of ScreenCaptureKit's surfaces (queueDepth 8, of which the
	// writer keeps one and the encoder a few).
	videoQueue = 3
	// audioQueue is how many audio buffers (20 ms each) may wait.
	audioQueue = 256
	// jitterWindow is how far behind now the mix is written: system audio
	// arrives a median 61 ms (at most about 80 ms) after its PTS.
	jitterWindow = 150 * time.Millisecond
	// tick is how often the mix is written.
	tick = 20 * time.Millisecond
	// stopTail is how long capture runs on after Stop is called, so the
	// audio from just before it arrives.
	stopTail = 200 * time.Millisecond
	// maxPending bounds the mix waiting for the encoder: 3 s.
	maxPending = 3 * mixer.Rate
)

// audioRate and audioChannels are the track's format: the mixer's.
const (
	audioRate     = mixer.Rate
	audioChannels = 2
)

// frame is a retained video sample on its way to the writer.
type frame struct {
	sbuf uintptr
	pts  int64 // host ns
}

// chunk is decoded audio on its way to the writer.
type chunk struct {
	src  mixer.Source
	pts  int64 // host ns
	rate float64
	pcm  [][]float32
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
	video  *mac.Stream
	sound  *mac.Stream // the second, audio-only stream (window recordings); nil otherwise
	writer *mac.Writer

	frames chan frame
	audio  chan chunk
	ctl    chan ctl

	skipped, queueDrops, audioDrops atomic.Int64

	stopOnce sync.Once
	finished chan struct{} // closed when the writer goroutine is done
	result   Result
	err      error
	stopped  atomic.Pointer[error] // a stream ended by itself
	// stopAt is the host time Stop was called (0 before): nothing captured
	// after it is written, though capture runs on a moment for the audio.
	stopAt atomic.Int64
}

// Start opens the file and starts recording.
func Start(cfg Config) (*Recorder, error) {
	switch {
	case cfg.Fragment == 0:
		cfg.Fragment = DefaultFragment
	case cfg.Fragment < 0:
		cfg.Fragment = 0
	}
	if cfg.Video.FPS <= 0 {
		cfg.Video.FPS = 30
	}
	fps := cfg.Video.FPS
	bitrate := cfg.Bitrate
	if bitrate <= 0 {
		bitrate = area.VideoBitrate(cfg.Video.Width, cfg.Video.Height, fps)
	}
	var audio *mac.AudioSettings
	if cfg.audio() {
		audio = &mac.AudioSettings{Rate: audioRate, Channels: audioChannels, Bitrate: area.AudioBitrate}
	}
	w, err := mac.NewWriter(cfg.Path, mac.VideoSettings{
		Width: cfg.Video.Width, Height: cfg.Video.Height, FPS: fps, Bitrate: bitrate,
	}, audio, cfg.Fragment)
	if err != nil {
		return nil, err
	}
	r := &Recorder{
		cfg: cfg, fps: fps, writer: w,
		frames:   make(chan frame, videoQueue),
		audio:    make(chan chunk, audioQueue),
		ctl:      make(chan ctl),
		finished: make(chan struct{}),
	}
	go r.run()

	video, sound := r.streams()
	cancel := func(err error) (*Recorder, error) {
		if r.video != nil {
			r.video.Stop()
		}
		done := make(chan struct{})
		r.ctl <- ctl{kind: ctlStop, at: -1, done: done} // -1: cancel
		<-done
		return nil, err
	}
	if r.video, err = mac.StartStream(video, r.sample, r.streamStopped); err != nil {
		return cancel(err)
	}
	if sound != nil {
		if r.sound, err = mac.StartStream(*sound, r.soundSample, r.streamStopped); err != nil {
			return cancel(fmt.Errorf("the audio stream: %w", err))
		}
	}
	slog.Info("record: started", "path", cfg.Path, "size", fmt.Sprintf("%d×%d", cfg.Video.Width, cfg.Video.Height),
		"fps", fps, "bitrate", bitrate, "fragment", cfg.Fragment, "systemAudio", cfg.SystemAudio, "mic", cfg.Mic,
		"secondStream", sound != nil)
	return r, nil
}

// streams splits the setup into the video stream and, when the video is a
// window (or reel hears only itself), an audio-only stream of the display.
func (r *Recorder) streams() (mac.StreamConfig, *mac.StreamConfig) {
	cfg := r.cfg
	video := cfg.Video
	video.SystemAudio, video.Mic, video.IncludeOwnAudio = false, "", false
	if !cfg.audio() {
		return video, nil
	}
	if video.Target.WindowID == 0 && !cfg.OwnAudioOnly {
		video.SystemAudio, video.Mic = cfg.SystemAudio, cfg.Mic
		return video, nil
	}
	// The second stream records the display at the smallest size it
	// allows, a frame a second; only its audio is used.
	sound := mac.StreamConfig{
		Target:          mac.Target{OwnAppOnly: cfg.OwnAudioOnly},
		Width:           2,
		Height:          2,
		FPS:             1,
		SystemAudio:     cfg.SystemAudio,
		IncludeOwnAudio: cfg.OwnAudioOnly,
		Mic:             cfg.Mic,
	}
	return video, &sound
}

// sample runs on ScreenCaptureKit's queue for the video stream: sort,
// retain or copy, hand over.
func (r *Recorder) sample(s mac.Sample) {
	switch s.Kind {
	case mac.KindScreen:
		r.frame(s)
	case mac.KindAudio:
		r.sound1(s, mixer.System, 2)
	case mac.KindMic:
		// A microphone array's first channel is the usable one; an
		// interface's first input is where a single mic goes.
		r.sound1(s, mixer.Mic, 1)
	}
}

// soundSample runs on ScreenCaptureKit's queue for the audio-only stream:
// its frames are ignored.
func (r *Recorder) soundSample(s mac.Sample) {
	if s.Kind != mac.KindScreen {
		r.sample(s)
	}
}

func (r *Recorder) frame(s mac.Sample) {
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
	case r.frames <- f:
	default:
		mac.Release(f.sbuf)
		r.queueDrops.Add(1)
	}
}

func (r *Recorder) sound1(s mac.Sample, src mixer.Source, keep int) {
	pts := s.PTS()
	if !pts.Valid() {
		return
	}
	c := chunk{src: src, pts: pts.Nanos()}
	err := s.PCM(func(f mac.ASBD, bufs [][]byte) {
		c.rate = f.SampleRate
		var err error
		c.pcm, err = mixer.Decode(mixer.Format{
			Rate: f.SampleRate, Channels: int(f.ChannelsPerFrame), Bits: int(f.BitsPerChannel),
			Float: f.Float(), Interleaved: f.Interleaved(), BigEndian: f.BigEndian(),
		}, bufs, keep, nil)
		if err != nil {
			c.pcm = nil
			slog.Warn("record: audio I can't read", "source", src, "format", f, "err", err)
		}
	})
	if err != nil || c.pcm == nil {
		return
	}
	select {
	case r.audio <- c:
	default:
		r.audioDrops.Add(int64(len(c.pcm[0])))
	}
}

// streamStopped hears a stream end by itself (the menu bar's stop
// control, sleep, the window closed): the recording ends there.
func (r *Recorder) streamStopped(err error) {
	if err == nil {
		return // Stop's own
	}
	r.stopped.CompareAndSwap(nil, &err)
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
		r.stopAt.Store(at)
		// Audio arrives after its PTS: keep capturing a moment so what was
		// said just before Stop makes it in.
		if r.cfg.audio() && r.stopped.Load() == nil {
			time.Sleep(stopTail)
		}
		for _, s := range []*mac.Stream{r.video, r.sound} {
			if s != nil {
				if err := s.Stop(); err != nil {
					slog.Warn("record: stop a stream", "err", err)
				}
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
	defer close(r.finished)
	w := writing{r: r, frameDur: int64(time.Second) / int64(r.fps), mix: mixer.New()}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case f := <-r.frames:
			mac.WithPool(func() { w.frame(f) })
		case c := <-r.audio:
			w.audio(c)
		case <-t.C:
			if r.cfg.audio() {
				mac.WithPool(func() { w.emit(mac.HostTime().Nanos() - int64(jitterWindow)) })
			}
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
	mix      *mixer.Mixer

	last    frame // the newest frame with pixels, retained (sbuf 0: none)
	lastOut int64 // the recording time of the last frame written
	wrote   bool
	err     error

	// pending is mixed audio the encoder had no room for yet.
	pending      []float32
	pendingStart int64 // its first frame
	audioLost    int64
}

// frame writes one frame at its place in the recording.
func (w *writing) frame(f frame) {
	if at := w.r.stopAt.Load(); at > 0 && f.pts > at {
		mac.Release(f.sbuf)
		return
	}
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

// audio places a buffer in the mix by its PTS.
func (w *writing) audio(c chunk) {
	if at := w.r.stopAt.Load(); at > 0 && c.pts > at {
		return
	}
	out, keep := w.tl.Map(c.pts)
	if !keep {
		return // before the first frame, or inside a pause
	}
	w.mix.Push(c.src, frames(out), c.rate, c.pcm)
}

// frames is a recording time (ns) in audio frames.
func frames(ns int64) float64 { return float64(ns) * audioRate / 1e9 }

// emit writes the mix up to host time upTo.
func (w *writing) emit(upTo int64) {
	if !w.tl.Started() || w.err != nil {
		return
	}
	if at := w.r.stopAt.Load(); at > 0 {
		upTo = min(upTo, at) // the recording ends there; Flush writes the rest
	}
	w.mix.Emit(int64(frames(w.tl.At(upTo))), w.queue)
	w.drain()
}

// queue adds mixed audio to what waits for the encoder.
func (w *writing) queue(start int64, block []float32) {
	if len(w.pending) == 0 {
		w.pendingStart = start
	}
	w.pending = append(w.pending, block...)
	if over := len(w.pending)/audioChannels - maxPending; over > 0 {
		// The encoder has been stuck for seconds: lose the oldest.
		w.pending = w.pending[over*audioChannels:]
		w.pendingStart += int64(over)
		w.audioLost += int64(over)
	}
}

// drain hands what waits to the encoder, as far as it takes it.
func (w *writing) drain() {
	for len(w.pending) > 0 && w.err == nil {
		n := min(len(w.pending)/audioChannels, mixer.Block)
		ok, err := w.r.writer.AppendAudio(w.pending[:n*audioChannels], audioChannels, audioRate, w.pendingStart)
		if err != nil {
			slog.Error("record: write audio", "err", err)
			w.err = err
			return
		}
		if !ok {
			return // no room yet: the next tick tries again
		}
		w.pending = w.pending[n*audioChannels:]
		w.pendingStart += int64(n)
	}
}

// resume ends the pause and writes the newest picture again where the
// recording picks up.
func (w *writing) resume(at int64) {
	w.tl.Resume(at)
	// A buffer that straddled the pause's start ran a few ms past it: the
	// sources pick up exactly where their PTS put them.
	w.mix.Reanchor()
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
	// What was captured before the stop still belongs in it.
	for drained := false; !drained; {
		select {
		case f := <-r.frames:
			if at >= 0 && f.pts <= at {
				w.frame(f)
			} else {
				mac.Release(f.sbuf)
			}
		case c := <-r.audio:
			if at >= 0 && c.pts <= at {
				w.audio(c)
			}
		default:
			drained = true
		}
	}
	res := Result{Path: r.cfg.Path, Skipped: int(r.skipped.Load())}
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
	// The newest frame is written again just before the end (below); when
	// it's too close for that, the end moves out to a frame past it.
	hold := end - w.frameDur
	if hold <= w.lastOut || w.last.sbuf == 0 {
		end = max(end, w.lastOut+w.frameDur)
		hold = -1
	}
	if r.cfg.audio() {
		w.mix.Flush(int64(frames(end)), w.queue)
		for i := 0; i < 200 && len(w.pending) > 0 && w.err == nil; i++ {
			w.drain()
			if len(w.pending) > 0 {
				time.Sleep(10 * time.Millisecond)
			}
		}
		w.audioLost += int64(len(w.pending) / audioChannels)
	}
	// The picture holds until the end: write the newest frame once more
	// just before it, lasting one frame.
	if hold >= 0 {
		for i := 0; i < 50; i++ { // the encoder may be busy for a moment
			before := r.writer.Appended
			w.write(w.last.sbuf, hold, mac.Nanos(w.frameDur))
			if r.writer.Appended > before || w.err != nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if w.err != nil {
		r.writer.Cancel()
		r.err = w.err
		return
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
	res.AudioFrames = r.writer.AudioFrames
	res.AudioLost = w.audioLost + r.audioDrops.Load() + w.mix.Late[mixer.System] + w.mix.Late[mixer.Mic]
	r.result = res
	slog.Info("record: finished", "path", res.Path, "duration", res.Duration, "frames", res.Frames,
		"dropped", res.Dropped, "skipped", res.Skipped, "audioFrames", res.AudioFrames, "audioLost", res.AudioLost,
		"systemLag", w.mix.Lag(mixer.System), "systemBendPPM", w.mix.Bend(mixer.System)*1e6,
		"micLag", w.mix.Lag(mixer.Mic), "micBendPPM", w.mix.Bend(mixer.Mic)*1e6,
		"jumps", w.mix.Reanchors)
}

// Feed hands the recording audio from somewhere other than
// ScreenCaptureKit: self-tests play a fake microphone this way, through
// the same path (pts is host time, pcm planar). Any goroutine.
func (r *Recorder) Feed(src mixer.Source, pts int64, rate float64, pcm [][]float32) {
	select {
	case r.audio <- chunk{src: src, pts: pts, rate: rate, pcm: pcm}:
	default:
		r.audioDrops.Add(int64(len(pcm[0])))
	}
}
