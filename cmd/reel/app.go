package main

// The app's windows: SDL, the GPU device, the text pipeline and the event
// loop. The main window is one surface; the floating controls while
// recording and the area selection are others, drawn by the same renderer.
// The loop sleeps in WaitEvent while nothing changes, so idle windows
// render no frames. Other goroutines publish state and wake the loop.
//
// Copied from an earlier project of the author's (without
// the voice session and the overlay), for several windows.

import (
	"flag"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Zyko0/go-sdl3/sdl"

	"github.com/anthonyrego/reel/internal/config"
	"github.com/anthonyrego/reel/internal/draw"
	"github.com/anthonyrego/reel/internal/gfx"
	"github.com/anthonyrego/reel/internal/logx"
	"github.com/anthonyrego/reel/internal/mac"
	"github.com/anthonyrego/reel/internal/platform"
	"github.com/anthonyrego/reel/internal/text"
	"github.com/anthonyrego/reel/internal/theme"
	"github.com/anthonyrego/reel/internal/ui"
	"github.com/anthonyrego/reel/internal/ui/home"
)

func init() {
	register(&command{name: "app", summary: "the reel window", run: runApp})
}

// scene is what a window draws. All methods run on the main thread.
type scene interface {
	// draw appends one frame for a w×h logical canvas.
	draw(c *ui.Ctx, l *draw.List, w, h float32)
	// event handles an input event and reports whether to redraw.
	event(c *ui.Ctx, ev ui.Event) bool
	// animating reports whether to keep drawing frames without input.
	animating() bool
	// woke takes new state when the loop is woken.
	woke()
}

// surface is a window and the scene it shows.
type surface struct {
	win     *gfx.Window
	id      sdl.WindowID
	scene   scene
	list    draw.List
	batch   gfx.Batch
	stalled bool // the last frame got no swapchain texture (minimized or covered)
	more    bool // the scene asked for another frame (ctx.Animate)
	dirty   bool
	// clear is the window's background (zero: the palette's).
	clear draw.Color
	// onClose, when set, runs on a close request instead of quitting.
	onClose func()
	// transparent clears frames to nothing: the window (made see-through
	// by platform.Float) draws its own shape.
	transparent bool
}

// winApp is the windows, their renderer and the text pipeline. All of its
// methods run on the main thread.
type winApp struct {
	device   *sdl.GPUDevice
	renderer *gfx.Renderer
	glyphs   *text.GlyphCache
	shaper   *text.Shaper
	pal      theme.Palette
	// theme is the theme asked for (see dark): "" follows the system.
	theme string

	main     *surface
	surfaces []*surface
	// window is the main window.
	window *gfx.Window

	frames  atomic.Int64
	cleanup []func()
}

const (
	themeLight = "light"
	themeDark  = "dark"
)

func runApp(args []string) error {
	fs := flag.NewFlagSet("app", flag.ContinueOnError)
	debug := fs.Bool("debug", false, "enable GPU validation")
	exitAfter := fs.Duration("exit-after", 0, "quit by itself after this long (unattended runs)")
	size := fs.String("size", "720x640", "window size in points, WxH")
	themeMode := fs.String("theme", "", "the window's theme, light|dark (default: the system's)")
	trimFile := fs.String("trim", "", "dev: open on the trim screen for this recording (unattended runs)")
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
	logf, err := logx.Setup(paths.Logs, "app", false)
	if err != nil {
		return err
	}
	defer logf.Close()

	cfg := gfx.WindowConfig{Title: "Reel", Hidden: true}
	if _, err := fmt.Sscanf(strings.ToLower(*size), "%dx%d", &cfg.Width, &cfg.Height); err != nil {
		return fmt.Errorf("-size %q: want WxH", *size)
	}
	a, err := openWindow(cfg, *debug)
	if err != nil {
		return err
	}
	defer a.close()
	a.window.Handle().SetMinimumSize(560, 520)
	switch *themeMode {
	case "", themeLight, themeDark:
		a.theme = *themeMode
	default:
		return fmt.Errorf("-theme %q: want light or dark", *themeMode)
	}
	a.pal = theme.For(a.dark())

	v := home.New()
	c := newController(paths, v)
	a.setScene(&homeScene{v: v, woken: c.woke, focused: c.refresh})
	hud := newHUD(a, c)
	c.onLive = hud.live
	c.selectArea = func() { selectArea(a, c) }
	c.openTrim = func(path string) { openTrim(a, c, path) }
	c.show()
	c.refresh()
	if *trimFile != "" {
		openTrim(a, c, *trimFile)
	}
	if err := platform.OnHotKey(func() { c.post(c.hotKey) }); err != nil {
		slog.Warn("app: "+platform.HotKeyName+" won't work", "err", err)
	}
	if err := mac.OnSleep(func(sleep bool) { c.post(func() { c.sleeping(sleep) }) }); err != nil {
		slog.Warn("app: sleep notifications", "err", err)
	}
	a.window.Handle().Show()
	err = a.run(*exitAfter)
	c.quit() // a recording in progress is finished, not lost
	return err
}

// openWindow sets up SDL, the GPU device, the main window and the text
// pipeline.
func openWindow(cfg gfx.WindowConfig, debug bool) (*winApp, error) {
	var err error
	if err := platform.Init(platform.Foreground); err != nil {
		return nil, err
	}
	a := &winApp{pal: theme.For(sdl.GetSystemTheme() == sdl.SYSTEM_THEME_DARK)}
	a.cleanup = append(a.cleanup, platform.Quit)
	platform.QuitOnSignal()

	fail := func(err error) (*winApp, error) { a.close(); return nil, err }
	if a.device, err = gfx.NewDevice(debug); err != nil {
		return fail(err)
	}
	a.cleanup = append(a.cleanup, a.device.Destroy)
	if a.renderer, err = gfx.NewRenderer(a.device); err != nil {
		return fail(err)
	}
	a.cleanup = append(a.cleanup, a.renderer.Destroy)
	if a.shaper, a.glyphs, err = newTextPipeline(); err != nil {
		return fail(err)
	}
	a.renderer.SetGlyphCache(a.glyphs)
	s, err := a.addWindow(cfg, nil)
	if err != nil {
		return fail(err)
	}
	a.main, a.window = s, s.win
	slog.Info("gpu", "driver", a.device.Driver(), "sdl", sdl.GetVersion().String(), "scale", s.win.Scale(), "density", s.win.PixelDensity())
	return a, nil
}

// addWindow opens another window showing sc.
func (a *winApp) addWindow(cfg gfx.WindowConfig, sc scene) (*surface, error) {
	w, err := gfx.NewWindow(a.device, cfg)
	if err != nil {
		return nil, err
	}
	id, err := w.Handle().ID()
	if err != nil {
		w.Destroy()
		return nil, err
	}
	s := &surface{win: w, id: id, scene: sc, dirty: true}
	s.batch.Glyphs = a.glyphs
	a.surfaces = append(a.surfaces, s)
	return s, nil
}

// removeWindow closes a window opened with addWindow.
func (a *winApp) removeWindow(s *surface) {
	for i, o := range a.surfaces {
		if o == s {
			a.surfaces = append(a.surfaces[:i], a.surfaces[i+1:]...)
			s.win.Destroy()
			return
		}
	}
}

// setScene sets what the main window shows.
func (a *winApp) setScene(sc scene) {
	a.main.scene, a.main.dirty = sc, true
}

func (a *winApp) close() {
	for _, s := range a.surfaces {
		s.win.Destroy()
	}
	a.surfaces = nil
	for i := len(a.cleanup) - 1; i >= 0; i-- {
		a.cleanup[i]()
	}
	a.cleanup = nil
}

// dark reports whether the windows draw dark: the theme asked for, else
// the system's.
func (a *winApp) dark() bool {
	switch a.theme {
	case themeLight:
		return false
	case themeDark:
		return true
	}
	return sdl.GetSystemTheme() == sdl.SYSTEM_THEME_DARK
}

// ctx is the widget context for a surface's frame.
func (a *winApp) ctx(s *surface) *ui.Ctx {
	return &ui.Ctx{Text: a.shaper, Pal: a.pal, Scale: s.win.Scale(), Now: time.Now(), Animate: func() { s.more = true }}
}

// surfaceOf is the surface an event belongs to (the main one for events
// that name no window).
func (a *winApp) surfaceOf(ev *sdl.Event) *surface {
	var id sdl.WindowID
	switch t := ev.Type; {
	case t >= sdl.EVENT_WINDOW_FIRST && t <= sdl.EVENT_WINDOW_LAST:
		id = ev.WindowEvent().WindowID
	case t == sdl.EVENT_MOUSE_MOTION:
		id = ev.MouseMotionEvent().WindowID
	case t == sdl.EVENT_MOUSE_BUTTON_DOWN || t == sdl.EVENT_MOUSE_BUTTON_UP:
		id = ev.MouseButtonEvent().WindowID
	case t == sdl.EVENT_MOUSE_WHEEL:
		id = ev.MouseWheelEvent().WindowID
	case t == sdl.EVENT_KEY_DOWN || t == sdl.EVENT_KEY_UP:
		id = ev.KeyboardEvent().WindowID
	case t == sdl.EVENT_TEXT_INPUT:
		id = ev.TextInputEvent().WindowID
	}
	for _, s := range a.surfaces {
		if s.id == id {
			return s
		}
	}
	return a.main
}

// run is the event loop. It sleeps in WaitEvent while nothing changes, so
// idle windows render no frames; while a scene animates the loop renders
// continuously, paced by the swapchain (vsync).
func (a *winApp) run(exitAfter time.Duration) error {
	start := time.Now()
	if exitAfter > 0 {
		time.AfterFunc(exitAfter, func() { sdl.PushEvent(&sdl.Event{Type: sdl.EVENT_QUIT}) })
	}
	quit := func() error {
		slog.Info("quit", "frames", a.frames.Load(), "uptime", time.Since(start).Round(time.Millisecond))
		return nil
	}

	// macOS runs its own event loop while the user drags a window edge, so
	// WaitEvent doesn't return; SDL delivers live-resize exposes to event
	// watchers on the main thread instead. The filter is created once:
	// purego never frees callbacks.
	watch := sdl.NewEventFilter(func(e *sdl.Event) bool {
		if e.Type == sdl.EVENT_WINDOW_EXPOSED && e.WindowEvent().Data1 == 1 {
			a.render(a.surfaceOf(e))
		}
		return true
	})
	if err := sdl.AddEventWatch(watch); err != nil {
		return err
	}

	all := func() {
		for _, s := range a.surfaces {
			s.dirty = true
		}
	}
	for {
		animating, stalled := false, false
		for _, s := range a.surfaces {
			if s.scene == nil {
				continue
			}
			if s.dirty || s.more || s.scene.animating() {
				a.render(s)
				s.dirty = false
			}
			if s.more || s.scene.animating() {
				animating = true
				stalled = stalled || s.stalled
			}
		}
		var ev sdl.Event
		switch {
		case animating && stalled:
			// No swapchain texture (minimized or covered): don't spin.
			if !sdl.WaitEventTimeout(&ev, 50) {
				continue
			}
		case animating:
			if !sdl.PollEvent(&ev) {
				continue
			}
		default:
			if err := sdl.WaitEvent(&ev); err != nil {
				return fmt.Errorf("wait event: %w", err)
			}
		}
		for {
			switch ev.Type {
			case sdl.EVENT_QUIT:
				return quit()
			case sdl.EVENT_WINDOW_CLOSE_REQUESTED:
				s := a.surfaceOf(&ev)
				if s == a.main || s.onClose == nil {
					return quit()
				}
				s.onClose()
			case sdl.EVENT_SYSTEM_THEME_CHANGED:
				a.pal = theme.For(a.dark())
				all()
			case sdl.EVENT_USER:
				platform.WakeTaken()
				// A woken scene may open or close windows: walk a copy.
				for _, s := range append([]*surface(nil), a.surfaces...) {
					if s.scene != nil {
						s.scene.woke()
					}
				}
				all()
			case sdl.EVENT_WINDOW_DISPLAY_SCALE_CHANGED:
				// Glyphs are keyed by pixel size: drop the old scale's.
				a.glyphs.Reset()
				all()
			case sdl.EVENT_WINDOW_FOCUS_GAINED:
				s := a.surfaceOf(&ev)
				// Back from System Settings (perhaps with a permission) or
				// from other apps (windows come and go).
				if f, ok := s.scene.(interface{ focus() }); ok {
					f.focus()
				}
				s.dirty = true
			case sdl.EVENT_WINDOW_SHOWN, sdl.EVENT_WINDOW_RESTORED, sdl.EVENT_WINDOW_EXPOSED,
				sdl.EVENT_WINDOW_PIXEL_SIZE_CHANGED, sdl.EVENT_WINDOW_DISPLAY_CHANGED:
				a.surfaceOf(&ev).dirty = true
			default:
				s := a.surfaceOf(&ev)
				if s.scene == nil {
					break
				}
				if uev, ok := a.translate(s, &ev); ok && s.scene.event(a.ctx(s), uev) {
					s.dirty = true
				}
			}
			if !sdl.PollEvent(&ev) {
				break
			}
		}
	}
}

// render draws and presents one frame of a surface.
func (a *winApp) render(s *surface) {
	if s.scene == nil {
		return
	}
	cmd, err := a.device.AcquireCommandBuffer()
	if err != nil {
		slog.Error("acquire command buffer", "err", err)
		return
	}
	sc, err := cmd.WaitAndAcquireGPUSwapchainTexture(s.win.Handle())
	if err != nil || sc == nil || sc.Texture == nil {
		// Minimized, hidden or occluded: nothing to draw into.
		cmd.Submit()
		s.stalled = true
		return
	}
	s.stalled = false
	pw, ph := int(sc.Width), int(sc.Height)
	scale := s.win.Scale()
	w, h := float32(pw)/scale, float32(ph)/scale
	s.more = false
	s.list.Reset()
	s.scene.draw(a.ctx(s), &s.list, w, h)
	s.batch.Reset(pw, ph)
	s.batch.AddList(&s.list, scale)
	if err := a.renderer.Prepare(cmd, &s.batch); err != nil {
		// Nothing was uploaded (dirty regions stay dirty for the next
		// frame); present a cleared frame rather than draw stale data.
		slog.Error("prepare", "err", err)
		s.batch.Reset(pw, ph)
	}
	clear := a.pal.Background
	switch {
	case s.transparent:
		clear = draw.Color{}
	case s.clear.A != 0:
		clear = s.clear
	}
	if err := a.renderer.Draw(cmd, sc.Texture, s.win.SwapchainFormat(), pw, ph, clear, &s.batch); err != nil {
		slog.Error("draw", "err", err)
	}
	if err := cmd.Submit(); err != nil {
		slog.Error("submit", "err", err)
	}
	a.frames.Add(1)
}
