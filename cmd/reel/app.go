package main

// The window: SDL, the GPU device, the text pipeline and the event loop.
// The loop sleeps in WaitEvent while nothing changes, so an idle window
// renders no frames. Other goroutines publish state and wake the loop.
//
// Copied from an earlier project of the author's (without
// the voice session and the overlay).

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
	"github.com/anthonyrego/reel/internal/platform"
	"github.com/anthonyrego/reel/internal/text"
	"github.com/anthonyrego/reel/internal/theme"
	"github.com/anthonyrego/reel/internal/ui"
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

// winApp is a window, its renderer and the text pipeline, drawing a scene.
// All of its methods run on the main thread.
type winApp struct {
	device   *sdl.GPUDevice
	window   *gfx.Window
	renderer *gfx.Renderer
	glyphs   *text.GlyphCache
	shaper   *text.Shaper
	list     draw.List
	batch    gfx.Batch
	pal      theme.Palette
	// theme is the theme asked for (see dark): "" follows the system.
	theme string
	scene scene
	// scale is the display scale glyphs were last rasterized for.
	scale   float32
	stalled bool // the last frame got no swapchain texture (minimized or covered)
	// more is set by a scene's ctx.Animate during a frame.
	more bool

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
	size := fs.String("size", "760x560", "window size in points, WxH")
	themeMode := fs.String("theme", "", "the window's theme, light|dark (default: the system's)")
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
	a.window.Handle().SetMinimumSize(520, 420)
	switch *themeMode {
	case "", themeLight, themeDark:
		a.theme = *themeMode
	default:
		return fmt.Errorf("-theme %q: want light or dark", *themeMode)
	}
	a.pal = theme.For(a.dark())

	h := newHome(func() { platform.Wake() })
	a.scene = h
	h.refresh()
	a.window.Handle().Show()
	return a.run(*exitAfter)
}

// openWindow sets up SDL, the GPU device, a window and the text pipeline.
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
	if a.window, err = gfx.NewWindow(a.device, cfg); err != nil {
		return fail(err)
	}
	a.cleanup = append(a.cleanup, a.window.Destroy)
	if a.renderer, err = gfx.NewRenderer(a.device); err != nil {
		return fail(err)
	}
	a.cleanup = append(a.cleanup, a.renderer.Destroy)
	if a.shaper, a.glyphs, err = newTextPipeline(); err != nil {
		return fail(err)
	}
	a.renderer.SetGlyphCache(a.glyphs)
	a.batch.Glyphs = a.glyphs
	a.scale = a.window.Scale()
	slog.Info("gpu", "driver", a.device.Driver(), "sdl", sdl.GetVersion().String(), "scale", a.window.Scale(), "density", a.window.PixelDensity())
	return a, nil
}

func (a *winApp) close() {
	for i := len(a.cleanup) - 1; i >= 0; i-- {
		a.cleanup[i]()
	}
	a.cleanup = nil
}

func (a *winApp) win() *gfx.Window { return a.window }

// dark reports whether the window draws dark: the theme it was asked for,
// else the system's.
func (a *winApp) dark() bool {
	switch a.theme {
	case themeLight:
		return false
	case themeDark:
		return true
	}
	return sdl.GetSystemTheme() == sdl.SYSTEM_THEME_DARK
}

// ctx is the widget context for this frame.
func (a *winApp) ctx() *ui.Ctx {
	return &ui.Ctx{Text: a.shaper, Pal: a.pal, Scale: a.win().Scale(), Now: time.Now(), Animate: func() { a.more = true }}
}

// run is the event loop. It sleeps in WaitEvent while nothing changes, so an
// idle window renders no frames; while the scene animates it renders
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
			a.render()
		}
		return true
	})
	if err := sdl.AddEventWatch(watch); err != nil {
		return err
	}

	dirty := true
	for {
		animating := a.more || a.scene.animating()
		if dirty || animating {
			a.render()
			dirty = false
			animating = a.more || a.scene.animating()
		}
		var ev sdl.Event
		switch {
		case animating && a.stalled:
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
			case sdl.EVENT_QUIT, sdl.EVENT_WINDOW_CLOSE_REQUESTED:
				return quit()
			case sdl.EVENT_SYSTEM_THEME_CHANGED:
				a.pal = theme.For(a.dark())
				dirty = true
			case sdl.EVENT_USER:
				platform.WakeTaken()
				a.scene.woke()
				dirty = true
			case sdl.EVENT_WINDOW_DISPLAY_SCALE_CHANGED:
				// Glyphs are keyed by pixel size; drop the old scale's so
				// the atlas refills at the new one.
				if sc := a.win().Scale(); sc != a.scale {
					a.glyphs.Reset()
					a.scale = sc
				}
				dirty = true
			case sdl.EVENT_WINDOW_FOCUS_GAINED:
				// Back from System Settings, perhaps with a permission.
				if r, ok := a.scene.(interface{ refresh() }); ok {
					r.refresh()
				}
				dirty = true
			case sdl.EVENT_WINDOW_SHOWN, sdl.EVENT_WINDOW_RESTORED, sdl.EVENT_WINDOW_EXPOSED,
				sdl.EVENT_WINDOW_PIXEL_SIZE_CHANGED, sdl.EVENT_WINDOW_DISPLAY_CHANGED:
				dirty = true
			default:
				if uev, ok := a.translate(&ev); ok && a.scene.event(a.ctx(), uev) {
					dirty = true
				}
			}
			if !sdl.PollEvent(&ev) {
				break
			}
		}
	}
}

// render draws and presents one frame.
func (a *winApp) render() {
	win := a.win()
	cmd, err := a.device.AcquireCommandBuffer()
	if err != nil {
		slog.Error("acquire command buffer", "err", err)
		return
	}
	sc, err := cmd.WaitAndAcquireGPUSwapchainTexture(win.Handle())
	if err != nil || sc == nil || sc.Texture == nil {
		// Minimized or occluded: nothing to draw into.
		cmd.Submit()
		a.stalled = true
		return
	}
	a.stalled = false
	pw, ph := int(sc.Width), int(sc.Height)
	scale := win.Scale()
	w, h := float32(pw)/scale, float32(ph)/scale
	a.more = false
	a.list.Reset()
	a.scene.draw(a.ctx(), &a.list, w, h)
	a.batch.Reset(pw, ph)
	a.batch.AddList(&a.list, scale)
	if err := a.renderer.Prepare(cmd, &a.batch); err != nil {
		// Nothing was uploaded (dirty regions stay dirty for the next
		// frame); present a cleared frame rather than draw stale data.
		slog.Error("prepare", "err", err)
		a.batch.Reset(pw, ph)
	}
	if err := a.renderer.Draw(cmd, sc.Texture, win.SwapchainFormat(), pw, ph, a.pal.Background, &a.batch); err != nil {
		slog.Error("draw", "err", err)
	}
	if err := cmd.Submit(); err != nil {
		slog.Error("submit", "err", err)
	}
	a.frames.Add(1)
}
