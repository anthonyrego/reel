package main

// The controller: the main window's state and what its controls do. It
// owns the settings, the devices, the recordings and the recording in
// progress, and builds the view's State from them. Everything here runs
// on the main thread; goroutines doing slow work (listing what can be
// recorded, starting and stopping a recording) post a func back with
// post, which runs when the loop wakes.

import (
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/anthonyrego/reel/internal/area"
	"github.com/anthonyrego/reel/internal/config"
	"github.com/anthonyrego/reel/internal/draw"
	"github.com/anthonyrego/reel/internal/library"
	"github.com/anthonyrego/reel/internal/mac"
	"github.com/anthonyrego/reel/internal/platform"
	"github.com/anthonyrego/reel/internal/record"
	"github.com/anthonyrego/reel/internal/settings"
	"github.com/anthonyrego/reel/internal/ui/controls"
	"github.com/anthonyrego/reel/internal/ui/home"
)

// The System Settings panes for each permission.
const (
	screenPane = "x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture"
	micPane    = "x-apple.systempreferences:com.apple.preference.security?Privacy_Microphone"
	cameraPane = "x-apple.systempreferences:com.apple.preference.security?Privacy_Camera"
)

// cameraWait bounds how long a recording waits for the camera's first
// picture (opening a camera takes about a second).
const cameraWait = 3 * time.Second

type controller struct {
	paths config.Paths
	set   settings.Settings
	v     *home.View
	// onLive hears a recording start (true) and end (false): the app
	// shows and hides its floating controls.
	onLive func(live bool)
	// selectArea lets the user drag out an area (the app's overlay).
	selectArea func()
	// openTrim shows the trim screen for a recording, openPlayer the
	// player screen.
	openTrim, openPlayer func(path string)
	// thumbs has the recordings' pictures (nil: none, as in renders).
	thumbs *thumbs
	// feed and bub are the camera and its bubble (nil: none, as in
	// renders and tests). fakeCam shows the fake camera instead of the
	// chosen one (agents), fakePick the choice made meanwhile.
	feed     *camFeed
	bub      *bubble
	fakeCam  bool
	fakePick string
	// offHome reports whether the main window shows another screen than
	// the list: trimming, playing.
	offHome func() bool
	// active: reel is the app in front.
	active bool
	// muted: the player plays without sound (kept while reel runs).
	muted bool

	mu      sync.Mutex
	pending []func()

	screen, micPerm mac.Permission
	content         mac.Content
	names           map[uint32]string
	mics            []mac.Mic
	camPerm         mac.Permission
	cams            []mac.Camera
	window          uint32 // the chosen window (not kept between runs)
	recs            []library.Recording

	rec       *record.Recorder
	recWindow uint32 // the window being recorded (0: not a window)
	// recCamera: the recording in progress (or starting) shows the
	// bubble, which stays in recRect, what it records (global points).
	recCamera bool
	recRect   draw.Rect
	starting  bool
	finishing bool
	stopTick  chan struct{}
	errText   string
	copied    string
	toast     string
	toastAt   time.Time
	gathering bool
	again     bool // a refresh was asked for while gathering
}

func newController(paths config.Paths, v *home.View) *controller {
	c := &controller{paths: paths, v: v, names: map[uint32]string{}, fakePick: fakeCamera}
	var err error
	if c.set, err = settings.Load(c.settingsPath()); err != nil {
		slog.Warn("app: settings unreadable; using the defaults", "err", err)
	}
	// Each action shows what it did as soon as it's done: the view's
	// State is built again from the controller.
	do := func(fn func()) func() { return func() { fn(); c.show() } }
	v.Act = home.Actions{
		AskScreen: do(c.askScreen), AskMic: do(c.askMic),
		SetTarget: shows(c, c.setTarget), PickDisplay: shows(c, c.pickDisplay), PickWindow: shows(c, c.pickWindow), PickMic: shows(c, c.pickMic),
		PickCamera:     shows(c, c.pickCamera),
		SetSystemAudio: shows(c, c.setSystemAudio),
		SelectArea: do(func() {
			if c.selectArea != nil {
				c.selectArea()
			}
		}),
		Record: do(c.record), Pause: do(c.pause), Resume: do(c.resume), Stop: do(c.stop),
		Copy: shows(c, c.copy), Reveal: shows(c, c.reveal), Open: shows(c, c.open), Delete: shows(c, c.delete), OpenFolder: do(c.openFolder),
		Trim: shows(c, func(path string) {
			if c.openTrim != nil {
				c.openTrim(path)
			}
		}),
		Play: shows(c, func(path string) {
			if c.openPlayer != nil {
				c.openPlayer(path)
			}
		}),
	}
	return c
}

// shows is fn, then the view's State built again to show what it did.
func shows[T any](c *controller, fn func(T)) func(T) {
	return func(v T) { fn(v); c.show() }
}

func (c *controller) settingsPath() string { return filepath.Join(c.paths.Data, "settings.json") }

func (c *controller) save() {
	if err := c.set.Save(c.settingsPath()); err != nil {
		slog.Warn("app: save the settings", "err", err)
	}
}

// post runs fn on the main thread when the loop next wakes. Any goroutine.
func (c *controller) post(fn func()) {
	c.mu.Lock()
	c.pending = append(c.pending, fn)
	c.mu.Unlock()
	platform.Wake()
}

// drew hears which recordings the window drew, so their pictures come
// first. Main thread.
func (c *controller) drew(visible []string) {
	if c.thumbs != nil {
		c.thumbs.want(c.recs, visible)
	}
}

// woke runs what was posted and shows the result. Main thread.
func (c *controller) woke() {
	c.mu.Lock()
	fns := c.pending
	c.pending = nil
	c.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
	c.show()
}

// refresh lists the permissions, what can be recorded, the mics and the
// recordings again, off the main thread. Asked for while it's gathering,
// it gathers again after.
func (c *controller) refresh() {
	if c.gathering {
		c.again = true
		return
	}
	c.gathering = true
	go func() {
		screen, micPerm, camPerm := mac.ScreenAccess(), mac.MicAccess(), mac.CameraAccess()
		var content mac.Content
		var cerr error
		if screen == mac.PermissionGranted {
			content, cerr = mac.ShareableContent()
		}
		mics, merr := mac.Mics()
		cams, camErr := mac.Cameras()
		recs, lerr := library.List(c.paths.Recordings)
		c.post(func() {
			c.gathering = false
			c.screen, c.micPerm, c.camPerm = screen, micPerm, camPerm
			if camErr == nil {
				c.cams = cams
			}
			if c.feed != nil && c.camWant().on() {
				c.feed.retry() // plugged back in, or allowed now
			}
			if cerr == nil {
				c.content = content
			} else {
				slog.Warn("app: shareable content", "err", cerr)
			}
			if merr == nil {
				c.mics = mics
			}
			if lerr == nil {
				c.recs = recs
			}
			c.names = mac.DisplayNames() // AppKit: main thread
			c.settle()
			if c.again {
				c.again = false
				c.refresh()
			}
		})
	}()
}

// settle fills in choices the user hasn't made yet: the main display, the
// system's default mic.
func (c *controller) settle() {
	if !c.set.MicChosen && len(c.mics) > 0 {
		for _, m := range c.mics {
			if m.Default {
				c.set.Mic, c.set.MicChosen = m.ID, true
				c.save()
			}
		}
	}
	if len(c.content.Displays) > 0 && c.display() == nil {
		c.set.Display = c.content.Displays[0].ID
	}
}

// display is the chosen display (nil when there's none).
func (c *controller) display() *mac.Display {
	for i, d := range c.content.Displays {
		if d.ID == c.set.Display {
			return &c.content.Displays[i]
		}
	}
	return nil
}

// show builds the view's State.
func (c *controller) show() {
	st := home.State{
		Screen:      perm(c.screen),
		Mic:         perm(c.micPerm),
		Target:      home.Target(c.set.Target),
		Display:     strconv.FormatUint(uint64(c.set.Display), 10),
		MicID:       c.set.Mic,
		SystemAudio: c.set.SystemAudio,
		HotKey:      platform.HotKeyName,
		Starting:    c.starting,
		Finishing:   c.finishing,
		Error:       c.errText,
		Copied:      c.copied,
		Toast:       c.toast,
		ToastAt:     c.toastAt,
	}
	if c.window != 0 {
		st.Window = strconv.FormatUint(uint64(c.window), 10)
	}
	for _, d := range c.content.Displays {
		name := c.names[d.ID]
		if name == "" {
			name = fmt.Sprintf("Display %d", d.ID)
		}
		st.Displays = append(st.Displays, controls.Choice{
			ID: strconv.FormatUint(uint64(d.ID), 10), Label: name, Detail: fmt.Sprintf("%.0f × %.0f", d.Frame.W, d.Frame.H),
		})
	}
	for _, w := range c.content.Windows {
		if !w.OnScreen {
			continue
		}
		st.Windows = append(st.Windows, controls.Choice{ID: strconv.FormatUint(uint64(w.ID), 10), Label: w.App, Detail: w.Title})
	}
	st.Camera, st.CameraID = perm(c.camPerm), c.cameraID()
	if c.fakeCam {
		st.Camera = home.PermOK
	}
	st.Cameras = append(st.Cameras, controls.Choice{ID: home.NoCamera, Label: "No camera"})
	known := c.cameraID() == home.NoCamera
	for _, cam := range c.cameraList() {
		detail := ""
		if cam.Kind == mac.CameraContinuity {
			detail = "iPhone"
		}
		st.Cameras = append(st.Cameras, controls.Choice{ID: cam.ID, Label: cam.Name, Detail: detail})
		known = known || cam.ID == st.CameraID
	}
	if !known {
		st.Cameras = append(st.Cameras, controls.Choice{ID: st.CameraID, Label: "Camera not connected"})
	}
	st.Mics = append(st.Mics, controls.Choice{ID: home.NoMic, Label: "No microphone"})
	for _, m := range c.mics {
		detail := ""
		switch {
		case m.Default:
			detail = "default"
		case m.Transport.Bluetooth():
			detail = "Bluetooth"
		}
		st.Mics = append(st.Mics, controls.Choice{ID: m.ID, Label: m.Name, Detail: detail})
	}
	if a := c.set.Area; a != nil {
		name := c.names[a.Display]
		if name == "" {
			name = fmt.Sprintf("display %d", a.Display)
		}
		st.AreaLabel = fmt.Sprintf("%.0f × %.0f at %.0f, %.0f on %s", a.W, a.H, a.X, a.Y, name)
	}
	st.Warning = c.warning()
	if c.rec != nil {
		paused, hidden := c.rec.Paused()
		st.Live = &home.Live{Elapsed: c.rec.Elapsed(), Paused: paused, Hidden: hidden}
	}
	for _, r := range c.recs {
		st.Recordings = append(st.Recordings, home.Recording{Path: r.Path, Name: r.Name, Duration: r.Duration, Size: r.Size, When: r.When, Thumb: c.thumbs.id(r)})
	}
	c.v.St = st
	c.syncCamera()
}

// cameraID is the chosen camera (home.NoCamera: none).
func (c *controller) cameraID() string {
	if c.fakeCam {
		return c.fakePick
	}
	return c.set.Camera
}

// cameraList is the cameras to choose from.
func (c *controller) cameraList() []mac.Camera {
	if c.fakeCam {
		return []mac.Camera{{ID: fakeCamera, Name: "Fake camera", Kind: mac.CameraExternal}}
	}
	return c.cams
}

// camWant is what decides whether the camera runs and the bubble shows.
type camWant struct {
	chosen, allowed bool // a camera is chosen, and reel may use it
	window          bool // the target is a window (which can't show it)
	// recording: one is in progress or starting; recCamera: it shows the
	// bubble.
	recording, recCamera bool
	active, home         bool // reel is in front, on the home screen
}

// on: while recording, if the recording shows the bubble; otherwise
// while reel is in front on the home screen, with a camera chosen and
// allowed, for a target that can show it.
func (w camWant) on() bool {
	if w.recording {
		return w.recCamera
	}
	return w.chosen && w.allowed && !w.window && w.active && w.home
}

func (c *controller) camWant() camWant {
	return camWant{
		chosen:    c.cameraID() != home.NoCamera,
		allowed:   c.fakeCam || c.camPerm == mac.PermissionGranted,
		window:    home.Target(c.set.Target) == home.Window,
		recording: c.rec != nil || c.starting,
		recCamera: c.recCamera,
		active:    c.active,
		home:      c.offHome == nil || !c.offHome(),
	}
}

// syncCamera opens the camera and shows the bubble, or closes and hides
// them, as camWant says. Main thread; cheap when nothing changes.
func (c *controller) syncCamera() {
	if c.feed == nil || c.bub == nil {
		return
	}
	if !c.camWant().on() {
		c.feed.want("")
		c.bub.hide()
		return
	}
	c.feed.want(c.cameraID())
	if !c.bub.shown() {
		c.bub.show(c.bubbleRect())
	}
}

// bubbleRect is where the bubble shows: where it was left (on a display
// still there), or the bottom-left of what's recorded.
func (c *controller) bubbleRect() draw.Rect {
	if b := c.set.Bubble; b != nil {
		r := draw.Rect{X: float32(b.X), Y: float32(b.Y), W: float32(b.Size), H: float32(b.Size)}
		return clampBubble(r, usableAt(r.X+r.W/2, r.Y+r.H/2), bubbleMin, bubbleMax)
	}
	_, usable, ok := c.targetRect()
	if !ok {
		usable = usableAt(0, 0)
	}
	return fitInto(draw.Rect{X: -1e6, W: bubbleDefault, H: bubbleDefault}, usable)
}

// targetRect is what the chosen display or area records, in global
// points, and the part of it the Dock and the menu bar leave (false for a
// window, or nothing chosen).
func (c *controller) targetRect() (recorded, usable draw.Rect, ok bool) {
	switch home.Target(c.set.Target) {
	case home.Area:
		a := c.set.Area
		if a == nil {
			return recorded, usable, false
		}
		for _, d := range c.content.Displays {
			if d.ID == a.Display {
				recorded = draw.Rect{X: float32(d.Frame.X + a.X), Y: float32(d.Frame.Y + a.Y), W: float32(a.W), H: float32(a.H)}
				ok = true
			}
		}
	case home.Display:
		if d := c.display(); d != nil {
			recorded = draw.Rect{X: float32(d.Frame.X), Y: float32(d.Frame.Y), W: float32(d.Frame.W), H: float32(d.Frame.H)}
			ok = true
		}
	}
	if !ok {
		return recorded, usable, false
	}
	usable = recorded.Intersect(usableAt(recorded.X+recorded.W/2, recorded.Y+recorded.H/2))
	if usable.Empty() {
		usable = recorded
	}
	return recorded, usable, true
}

// fitBubble moves the bubble into what's recorded now (a different
// display or area was chosen), where it would be recorded.
func (c *controller) fitBubble() {
	if c.cameraID() == home.NoCamera {
		return
	}
	_, usable, ok := c.targetRect()
	if !ok {
		return
	}
	r := c.bubbleRect()
	if c.bub != nil && c.bub.shown() {
		r = c.bub.r
	}
	if fit := fitInto(r, usable); fit != r {
		c.keepBubble(fit, c.bubbleLook())
		if c.bub != nil && c.bub.shown() {
			c.bub.place(fit)
		}
	}
}

// keepBubble saves where the bubble is and how it looks.
func (c *controller) keepBubble(r draw.Rect, k bubbleLook) {
	c.set.Bubble = &settings.Bubble{X: float64(r.X), Y: float64(r.Y), Size: float64(r.W),
		Zoom: k.crop.Zoom, PanX: k.crop.X, PanY: k.crop.Y, Round: float64(k.round)}
	c.save()
}

// bubbleLook is how the bubble looks: as it was left.
func (c *controller) bubbleLook() bubbleLook {
	if c.bub != nil {
		return c.bub.look
	}
	return lookOf(c.set.Bubble)
}

// recorded is the rect a recording showing the bubble keeps it in.
func (c *controller) recorded() (draw.Rect, bool) {
	return c.recRect, c.recCamera && (c.rec != nil || c.starting)
}

func perm(p mac.Permission) home.Perm {
	switch p {
	case mac.PermissionGranted:
		return home.PermOK
	case mac.PermissionUnknown:
		return home.PermAsk
	}
	return home.PermDenied
}

// warning is a line about the camera that failed, or the sound setup that
// will disappoint.
func (c *controller) warning() string {
	if c.feed != nil && c.camWant().on() {
		if st, err := c.feed.status(); st == camFailed {
			switch {
			case errors.Is(err, mac.ErrNotBundle):
				return "The camera works only in Reel.app (make run): this is the bare binary."
			case errors.Is(err, mac.ErrNoCameraAccess):
				return "Reel isn't allowed to use the camera: allow it in System Settings › Camera."
			case err != nil:
				return "The camera stopped: " + err.Error()
			}
			return "The camera stopped."
		}
	}
	for _, m := range c.mics {
		if m.ID != c.set.Mic {
			continue
		}
		switch {
		case m.Transport.Bluetooth():
			return "A Bluetooth mic switches the headset to call quality while it records."
		case m.Transport == mac.TransportBuiltIn && c.set.SystemAudio:
			return "On the speakers, the mic hears the system audio too: wear headphones."
		}
	}
	return ""
}

func (c *controller) say(msg string) {
	c.toast, c.toastAt = msg, time.Now()
}

func (c *controller) askScreen() {
	if !mac.RequestScreenAccess() {
		openURL(screenPane)
	}
	c.say("Allow Reel, then quit and open it again")
}

func (c *controller) askMic() {
	if c.micPerm == mac.PermissionDenied {
		openURL(micPane)
		return
	}
	if err := mac.RequestMicAccess(func(bool) { c.post(c.refresh) }); err != nil {
		slog.Error("app: mic access", "err", err)
	}
}

func (c *controller) setTarget(t home.Target) {
	c.set.Target = string(t)
	c.save()
	if t == home.Window {
		c.refresh() // windows come and go
	}
	c.fitBubble()
}

func (c *controller) pickDisplay(id string) {
	n, _ := strconv.ParseUint(id, 10, 32)
	c.set.Display = uint32(n)
	c.save()
	c.fitBubble()
}

func (c *controller) pickWindow(id string) {
	n, _ := strconv.ParseUint(id, 10, 32)
	c.window = uint32(n)
}

func (c *controller) pickMic(id string) {
	c.set.Mic, c.set.MicChosen = id, true
	c.save()
}

// pickCamera chooses the bubble's camera. The first choice of one asks
// for the Camera permission (only Reel.app can); a denied one opens
// System Settings.
func (c *controller) pickCamera(id string) {
	if c.fakeCam {
		c.fakePick = id
		return
	}
	c.set.Camera = id
	c.save()
	if id == home.NoCamera {
		return
	}
	c.fitBubble()
	switch c.camPerm {
	case mac.PermissionUnknown:
		if err := mac.RequestCameraAccess(func(bool) { c.post(c.refresh) }); err != nil {
			c.say("Only Reel.app can use the camera (make run)")
		}
	case mac.PermissionDenied:
		openURL(cameraPane)
		c.say("Allow Reel under Camera, then come back")
	}
}

func (c *controller) setSystemAudio(on bool) {
	c.set.SystemAudio = on
	c.save()
}

// recordConfig is the recording the choices describe.
func (c *controller) recordConfig() (record.Config, error) {
	cfg := record.Config{
		Video:       mac.StreamConfig{FPS: c.set.FPS, ShowsCursor: true},
		SystemAudio: c.set.SystemAudio,
		Path:        library.NewPath(c.paths.Recordings, time.Now()),
	}
	if c.micPerm == mac.PermissionGranted {
		cfg.Mic = c.set.Mic
	}
	switch home.Target(c.set.Target) {
	case home.Window:
		for _, w := range c.content.Windows {
			if w.ID == c.window {
				cfg.Video.Target = mac.Target{WindowID: w.ID}
				cfg.Video.Width, cfg.Video.Height = area.Pixels(w.Frame.W, w.Frame.H, scaleAt(c.content, w.Frame), area.MaxW, area.MaxH)
				return cfg, nil
			}
		}
		return cfg, errors.New("choose a window first")
	case home.Area:
		a := c.set.Area
		if a == nil {
			return cfg, errors.New("select an area first")
		}
		for _, d := range c.content.Displays {
			if d.ID == a.Display {
				cfg.Video.Target = mac.Target{DisplayID: d.ID}
				cfg.Video.SourceRect = mac.Rect{X: a.X, Y: a.Y, W: a.W, H: a.H}
				cfg.Video.Width, cfg.Video.Height = area.Pixels(a.W, a.H, d.Scale, area.MaxW, area.MaxH)
				return cfg, nil
			}
		}
		return cfg, errors.New("the area's display isn't connected")
	default:
		d := c.display()
		if d == nil {
			return cfg, errors.New("no display to record")
		}
		cfg.Video.Target = mac.Target{DisplayID: d.ID}
		cfg.Video.Width, cfg.Video.Height = area.Pixels(d.Frame.W, d.Frame.H, d.Scale, area.MaxW, area.MaxH)
		return cfg, nil
	}
}

// record starts a recording. One that shows the bubble (a display or
// an area, with a camera chosen and allowed: from another app too) moves
// it into what's recorded, opens the camera and waits for its first
// picture, so the recording starts with it.
func (c *controller) record() {
	if c.rec != nil || c.starting || c.finishing {
		return
	}
	cfg, err := c.recordConfig()
	if err != nil {
		c.errText = err.Error()
		return
	}
	c.errText, c.starting = "", true
	w := c.camWant()
	w.recording, w.active, w.home = false, true, true
	recorded, usable, ok := c.targetRect()
	c.recCamera = c.feed != nil && c.bub != nil && ok && w.on()
	if !c.recCamera {
		c.start(cfg)
		return
	}
	c.recRect = recorded
	c.syncCamera()
	if fit := fitInto(c.bub.r, usable); fit != c.bub.r {
		c.bub.place(fit)
		c.keepBubble(fit, c.bub.look)
	}
	c.feed.ready(cameraWait, func(ok bool) {
		n, err := c.bub.windowNumber()
		if err != nil {
			slog.Warn("app: the bubble's window", "err", err)
		} else {
			cfg.Video.Target.Show = []uint32{n}
		}
		if lag, got := c.feed.lag(); ok && got {
			cfg.MicDelay = lag + bubbleDelay(c.bub.refresh())
		} else {
			slog.Warn("app: no picture from the camera yet: recording anyway")
		}
		c.start(cfg)
	})
}

// start starts the recording cfg describes, off the main thread.
func (c *controller) start(cfg record.Config) {
	go func() {
		r, err := record.Start(cfg)
		c.post(func() {
			c.starting = false
			if err != nil {
				c.errText = "Couldn't start: " + err.Error()
				slog.Error("app: record", "err", err)
				c.recCamera = false
				return
			}
			c.rec, c.recWindow = r, cfg.Video.Target.WindowID
			c.startTicking(r)
			if c.onLive != nil {
				c.onLive(true)
			}
		})
	}()
}

// startTicking redraws the clock while the recording runs, watches a
// recorded window (ScreenCaptureKit doesn't say when one hides) and
// notices the recording ending by itself.
func (c *controller) startTicking(r *record.Recorder) {
	stop := make(chan struct{})
	c.stopTick = stop
	window := c.recWindow
	go func() {
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		gone := false
		for {
			select {
			case <-stop:
				return
			case <-r.Done():
				c.post(func() { c.finished(r) })
				return
			case <-t.C:
				if window != 0 {
					on, _ := mac.WindowOnScreen(window)
					if on == gone { // it changed
						gone = !on
						r.ContentGone(gone)
					}
				}
				c.post(func() {})
			}
		}
	}()
}

func (c *controller) pause() {
	if r := c.rec; r != nil {
		go func() { r.Pause(); c.post(func() {}) }()
	}
}

func (c *controller) resume() {
	if r := c.rec; r != nil {
		go func() { r.Resume(); c.post(func() {}) }()
	}
}

// stop ends the recording; it's finished (and copied) off the main thread.
func (c *controller) stop() {
	r := c.rec
	if r == nil || c.finishing {
		return
	}
	c.finishing = true
	go func() {
		r.Stop()
		c.post(func() { c.finished(r) })
	}()
}

// finished takes a recording that has ended (by Stop or by itself): the
// file goes on the clipboard and into the list.
func (c *controller) finished(r *record.Recorder) {
	if c.rec != r {
		return // already taken
	}
	if c.stopTick != nil {
		close(c.stopTick)
		c.stopTick = nil
	}
	c.rec, c.recWindow, c.finishing, c.recCamera = nil, 0, false, false
	if c.onLive != nil {
		c.onLive(false)
	}
	res, err := r.Stop() // done already: returns the result
	if err != nil {
		c.errText = "The recording failed: " + err.Error()
		slog.Error("app: recording failed", "err", err)
		c.refresh()
		return
	}
	if res.StoppedBy != nil {
		c.errText = "The recording stopped by itself (" + res.StoppedBy.Error() + "); what it got is saved."
	}
	if err := mac.CopyFile(res.Path); err != nil {
		c.say("Saved (couldn't copy it: " + err.Error() + ")")
	} else {
		c.copied = res.Path
		c.say("Copied: paste it into Slack")
	}
	slog.Info("app: recording saved", "path", res.Path, "duration", res.Duration, "lostAudio", res.AudioLost, "droppedFrames", res.Dropped)
	c.refresh()
}

// quit finishes a recording in progress before the app goes.
func (c *controller) quit() {
	if r := c.rec; r != nil {
		if _, err := r.Stop(); err != nil {
			slog.Error("app: finish at quit", "err", err)
		}
		c.finished(r)
	}
}

// hotKey starts or stops, from any app.
func (c *controller) hotKey() {
	if c.rec != nil {
		c.stop()
	} else {
		c.record()
	}
}

// sleeping pauses a recording across a sleep (the user resumes it).
func (c *controller) sleeping(sleep bool) {
	if sleep && c.rec != nil {
		slog.Info("app: the Mac is going to sleep: pausing")
		c.pause()
	}
}

func (c *controller) copy(path string) {
	if err := mac.CopyFile(path); err != nil {
		c.say("Couldn't copy it: " + err.Error())
		return
	}
	c.copied = path
	c.say("Copied: paste it into Slack")
}

func (c *controller) reveal(path string) { run("open", "-R", path) }
func (c *controller) open(path string)   { run("open", path) }
func (c *controller) openFolder()        { run("open", c.paths.Recordings) }

func (c *controller) delete(path string) {
	if err := mac.Trash(path); err != nil {
		c.say("Couldn't move it to the Trash: " + err.Error())
		return
	}
	if c.copied == path {
		c.copied = ""
	}
	c.say("Moved to the Trash")
	c.refresh()
}

func run(name string, args ...string) {
	if err := exec.Command(name, args...).Start(); err != nil {
		slog.Error("app: "+name, "args", args, "err", err)
	}
}

// openURL opens a URL with the system's handler.
func openURL(u string) { run("open", u) }

// setArea keeps an area the user dragged out (display points) and records
// it from now on.
func (c *controller) setArea(display uint32, x, y, w, h float64) {
	c.set.Area = &settings.Area{Display: display, X: x, Y: y, W: w, H: h}
	c.set.Target = string(home.Area)
	c.save()
	c.fitBubble()
}
