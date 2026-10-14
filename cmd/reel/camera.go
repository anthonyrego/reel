package main

// The camera feed: the camera the bubble shows, started and stopped by one
// worker (opening a camera takes about a second and blocks, so app
// switches that want it on and off never overlap two of them), and its
// frames cut to the bubble's size off the main thread and handed over
// for the next frame the bubble draws. A fake camera (agents: renders,
// short app runs, the self-test) feeds the same path from the cut on.

import (
	"fmt"
	"image"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anthonyrego/reel/internal/mac"
	"github.com/anthonyrego/reel/internal/platform"
	"github.com/anthonyrego/reel/internal/thumb"
)

// fakeCamera is the camera ID that opens the fake camera.
const fakeCamera = "fake"

// bubbleRefreshes is how many of its display's refreshes after the
// camera hands a frame over it's in a recording: the cut, the bubble's
// next frame on the main thread, the compositor and ScreenCaptureKit's
// capture. selftest -camera measures it: 3.3 to 4.1 refreshes (111 to
// 138 ms) in five runs at 30 Hz (2026-10-14). With the camera's own lag
// (each frame's arrival less its PTS, measured live) it's how much later
// the mic is placed, so the voice meets the face.
const bubbleRefreshes = 3.9

// bubbleDelay is bubbleRefreshes at a display's refresh interval.
func bubbleDelay(refresh time.Duration) time.Duration {
	return time.Duration(bubbleRefreshes * float64(refresh))
}

// camFeed is the bubble's camera.
type camFeed struct {
	post func(func()) // runs a func on the main thread
	// flash, when set, lights the fake camera's picture at a host time
	// (the self-test's flashes).
	flash func(host int64) bool

	// size is the bubble's side in pixels, which frames are cut to (the
	// main thread sets it).
	size atomic.Int32

	kick chan struct{} // the wanted camera changed
	open atomic.Bool   // the worker has a camera open

	// The camera's queue's (or the fake's goroutine's) alone.
	cut  thumb.Bubble
	back *image.RGBA

	mu    sync.Mutex
	id    string // the camera wanted ("" none): what the state is about
	again bool   // open it again though it's the one open (it failed)
	front *image.RGBA
	fresh bool // the front frame isn't taken by the bubble yet
	state camState
	err   error
	first chan struct{} // closed at the camera's first frame
	// How it's doing, for the log and the mic's delay.
	frames            int
	firstPTS, lastPTS int64
	w, h              int
	lags              []int64 // the latest frames' arrival less PTS, ns
	lagAt             int
	since             time.Time
}

// camState is what the camera is doing.
type camState int

const (
	camOff      camState = iota
	camStarting          // wanted, no frame yet
	camLive              // frames coming
	camFailed            // it wouldn't start, or stopped by itself
)

// lagWindow is how many frames the lag is the median of.
const lagWindow = 64

func newCamFeed(post func(func())) *camFeed {
	f := &camFeed{post: post, kick: make(chan struct{}, 1)}
	go f.work()
	return f
}

// want asks for a camera ("" for none; fakeCamera for the fake): the
// worker closes any other and opens it. The newest wish wins; asking
// again for the one wanted does nothing (a failed camera is tried again
// by retry). Main thread.
func (f *camFeed) want(id string) {
	f.mu.Lock()
	if id == f.id {
		f.mu.Unlock()
		return
	}
	f.logStats()
	f.id, f.state, f.err, f.fresh = id, camOff, nil, false
	if id != "" {
		f.state, f.first = camStarting, make(chan struct{})
		f.frames, f.lags, f.lagAt, f.since = 0, f.lags[:0], 0, time.Now()
	}
	f.mu.Unlock()
	f.wake()
}

// retry opens the wanted camera again if it failed (it may work now: the
// permission was just granted). Main thread.
func (f *camFeed) retry() {
	f.mu.Lock()
	if f.state != camFailed || f.id == "" {
		f.mu.Unlock()
		return
	}
	f.again, f.state, f.err, f.first = true, camStarting, nil, make(chan struct{})
	f.frames, f.lags, f.lagAt, f.since = 0, f.lags[:0], 0, time.Now()
	f.mu.Unlock()
	f.wake()
}

func (f *camFeed) wake() {
	select {
	case f.kick <- struct{}{}:
	default:
	}
}

// work opens and closes cameras, one at a time: opening one blocks for
// about a second.
func (f *camFeed) work() {
	var cam *mac.CameraStream
	var stopFake func()
	running := ""
	for range f.kick {
		f.mu.Lock()
		id, again := f.id, f.again
		f.again = false
		f.mu.Unlock()
		if id == running && !again {
			continue
		}
		if cam != nil {
			cam.Stop()
			cam = nil
		}
		if stopFake != nil {
			stopFake()
			stopFake = nil
		}
		running = id
		switch id {
		case "":
		case fakeCamera:
			stopFake = f.runFake()
		default:
			var err error
			cam, err = mac.StartCamera(id, func(s mac.Sample) { f.sample(id, s) }, func(err error) { f.failed(id, err) })
			if err != nil {
				f.failed(id, err)
			}
		}
		f.open.Store(running != "")
	}
}

// failed hears a camera not start, or stop by itself.
func (f *camFeed) failed(id string, err error) {
	slog.Warn("camera: stopped", "err", err)
	f.mu.Lock()
	if f.id == id {
		f.state, f.err = camFailed, err
	}
	f.mu.Unlock()
	f.post(func() {})
}

// sample is a real camera's frame, on its queue: cut it to the bubble.
func (f *camFeed) sample(id string, s mac.Sample) {
	arrived := mac.HostTime().Nanos()
	pts := s.PTS()
	if !pts.Valid() {
		return
	}
	s.Pixels(func(pix []byte, w, h, stride int) { f.frame(id, pix, w, h, stride, pts.Nanos(), arrived) })
}

// frame cuts a BGRA frame from camera id to the bubble and hands it over,
// from the camera's queue or the fake's goroutine (one camera at a time).
func (f *camFeed) frame(id string, pix []byte, w, h, stride int, pts, arrived int64) {
	d := int(f.size.Load())
	if d <= 0 {
		return
	}
	if f.back == nil || f.back.Rect.Dx() != d {
		f.back = image.NewRGBA(image.Rect(0, 0, d, d))
	}
	f.cut.Cut(f.back, pix, w, h, stride, thumb.Crop{})
	f.mu.Lock()
	if f.id != id || f.state == camFailed {
		f.mu.Unlock()
		return // a camera on its way out
	}
	f.front, f.back = f.back, f.front
	f.fresh = true
	if f.state == camStarting {
		f.state, f.firstPTS = camLive, pts
		close(f.first)
	}
	f.frames++
	f.lastPTS, f.w, f.h = pts, w, h
	if len(f.lags) < lagWindow {
		f.lags = append(f.lags, arrived-pts)
	} else {
		f.lags[f.lagAt] = arrived - pts
		f.lagAt = (f.lagAt + 1) % lagWindow
	}
	first := f.frames == 1
	f.mu.Unlock()
	if first {
		f.post(func() {}) // the bubble shows the picture, not the placeholder
	}
	platform.WakeForFrame()
}

// take hands the newest frame to fn if one came since the last take: the
// bubble uploads it. Main thread; fn runs with the frame held.
func (f *camFeed) take(fn func(img *image.RGBA)) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.fresh || f.front == nil {
		return false
	}
	f.fresh = false
	fn(f.front)
	return true
}

// status is what the wanted camera is doing, and why it failed.
func (f *camFeed) status() (camState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state, f.err
}

// lag is the median of the latest frames' arrival less their PTS: how
// late the camera hands over what it saw (false before any frame).
func (f *camFeed) lag() (time.Duration, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.lags) == 0 {
		return 0, false
	}
	l := slices.Clone(f.lags)
	slices.Sort(l)
	return time.Duration(l[len(l)/2]), true
}

// ready calls done on the main thread once the wanted camera has sent a
// frame (true), or has failed or taken longer than timeout (false).
func (f *camFeed) ready(timeout time.Duration, done func(ok bool)) {
	f.mu.Lock()
	first, st := f.first, f.state
	f.mu.Unlock()
	switch st {
	case camLive:
		done(true)
		return
	case camStarting:
	default:
		done(false)
		return
	}
	go func() {
		ok := false
		select {
		case <-first:
			ok = true
		case <-time.After(timeout):
		}
		f.post(func() {
			st, _ := f.status()
			done(ok && st == camLive)
		})
	}()
}

// logStats says how the camera did as it's closed: what it sent and how
// late (with its PTS on the host clock, a few tens of ms). With f.mu.
func (f *camFeed) logStats() {
	if f.id == "" {
		return
	}
	on := time.Since(f.since).Round(time.Millisecond)
	if f.frames < 2 {
		slog.Info("camera: closed", "frames", f.frames, "on", on, "err", f.err)
		return
	}
	l := slices.Clone(f.lags)
	slices.Sort(l)
	slog.Info("camera: closed", "frames", f.frames, "fps", float64(f.frames-1)/time.Duration(f.lastPTS-f.firstPTS).Seconds(),
		"size", fmt.Sprintf("%d×%d", f.w, f.h), "lagP50", time.Duration(l[len(l)/2]), "lagP95", time.Duration(l[len(l)*95/100]),
		"on", on, "fake", f.id == fakeCamera)
}

// stop closes the camera and waits for the worker to have done it (at
// quit).
func (f *camFeed) stop() {
	f.want("")
	for i := 0; i < 300 && f.open.Load(); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if f.open.Load() {
		slog.Warn("camera: still open at quit")
	}
}

// The fake camera's frames: 1280×720 BGRA at 30 a second.
const (
	fakeCamW, fakeCamH = 1280, 720
	fakeCamFPS         = 30
)

// runFake feeds the fake camera until the returned func is called: a
// picture with a marker in the top left of the middle (so the mirror
// shows) and a bar sweeping across, all white while flash says so, each
// frame stamped with the host time it's "captured" at on a 30 fps grid.
func (f *camFeed) runFake() (stop func()) {
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		pix := make([]byte, fakeCamW*fakeCamH*4)
		start := mac.HostTime().Nanos()
		const period = int64(time.Second) / fakeCamFPS
		for k := int64(0); ; k++ {
			at := start + k*period
			if wait := time.Duration(at - mac.HostTime().Nanos()); wait > 0 {
				select {
				case <-done:
					return
				case <-time.After(wait):
				}
			}
			select {
			case <-done:
				return
			default:
			}
			lit := f.flash != nil && f.flash(at)
			fakePicture(pix, int(k), lit)
			f.frame(fakeCamera, pix, fakeCamW, fakeCamH, fakeCamW*4, at, mac.HostTime().Nanos())
		}
	}()
	return func() { close(done); <-finished }
}

// fakePicture draws the fake camera's frame k into pix (BGRA).
func fakePicture(pix []byte, k int, lit bool) {
	if lit {
		for i := range pix {
			pix[i] = 0xff
		}
		return
	}
	s := fakeCamH
	ox := (fakeCamW - s) / 2
	bar := ox + (k*8)%s
	for y := range fakeCamH {
		row := pix[y*fakeCamW*4:]
		for x := range fakeCamW {
			// A dusk gradient, the marker (top left of the middle square)
			// and the sweeping bar.
			b, g, r := uint8(90+y*60/fakeCamH), uint8(50+y*40/fakeCamH), uint8(40+x*50/fakeCamW)
			switch {
			case x >= ox+s/8 && x < ox+s/8+s/5 && y >= s/8 && y < s/8+s/5:
				b, g, r = 60, 190, 250
			case x >= bar && x < bar+12:
				b, g, r = 230, 230, 230
			}
			o := x * 4
			row[o], row[o+1], row[o+2], row[o+3] = b, g, r, 0xff
		}
	}
}
