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
)

type controller struct {
	paths config.Paths
	set   settings.Settings
	v     *home.View
	// onLive hears a recording start (true) and end (false): the app
	// shows and hides its floating controls.
	onLive func(live bool)
	// selectArea lets the user drag out an area (the app's overlay).
	selectArea func()
	// openTrim shows the trim screen for a recording.
	openTrim func(path string)
	// thumbs has the recordings' pictures (nil: none, as in renders).
	thumbs *thumbs

	mu      sync.Mutex
	pending []func()

	screen, micPerm mac.Permission
	content         mac.Content
	names           map[uint32]string
	mics            []mac.Mic
	window          uint32 // the chosen window (not kept between runs)
	recs            []library.Recording

	rec       *record.Recorder
	recWindow uint32 // the window being recorded (0: not a window)
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
	c := &controller{paths: paths, v: v, names: map[uint32]string{}}
	var err error
	if c.set, err = settings.Load(c.settingsPath()); err != nil {
		slog.Warn("app: settings unreadable; using the defaults", "err", err)
	}
	v.Act = home.Actions{
		AskScreen: c.askScreen, AskMic: c.askMic,
		SetTarget: c.setTarget, PickDisplay: c.pickDisplay, PickWindow: c.pickWindow, PickMic: c.pickMic,
		SetSystemAudio: c.setSystemAudio,
		SelectArea: func() {
			if c.selectArea != nil {
				c.selectArea()
			}
		},
		Record: c.record, Pause: c.pause, Resume: c.resume, Stop: c.stop,
		Copy: c.copy, Reveal: c.reveal, Open: c.open, Delete: c.delete, OpenFolder: c.openFolder,
		Trim: func(path string) {
			if c.openTrim != nil {
				c.openTrim(path)
			}
		},
	}
	return c
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
		screen, micPerm := mac.ScreenAccess(), mac.MicAccess()
		var content mac.Content
		var cerr error
		if screen == mac.PermissionGranted {
			content, cerr = mac.ShareableContent()
		}
		mics, merr := mac.Mics()
		recs, lerr := library.List(c.paths.Recordings)
		c.post(func() {
			c.gathering = false
			c.screen, c.micPerm = screen, micPerm
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
		Finishing:   c.finishing || c.starting,
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

// warning is a line about the sound setup that will disappoint.
func (c *controller) warning() string {
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
}

func (c *controller) pickDisplay(id string) {
	n, _ := strconv.ParseUint(id, 10, 32)
	c.set.Display = uint32(n)
	c.save()
}

func (c *controller) pickWindow(id string) {
	n, _ := strconv.ParseUint(id, 10, 32)
	c.window = uint32(n)
}

func (c *controller) pickMic(id string) {
	c.set.Mic, c.set.MicChosen = id, true
	c.save()
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

// record starts a recording.
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
	go func() {
		r, err := record.Start(cfg)
		c.post(func() {
			c.starting = false
			if err != nil {
				c.errText = "Couldn't start: " + err.Error()
				slog.Error("app: record", "err", err)
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
	c.rec, c.recWindow, c.finishing = nil, 0, false
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
}
