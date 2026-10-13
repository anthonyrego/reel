package main

// Offscreen rendering: the render command, and the way an agent looks at
// what it built without opening a window.
//
// Copied from an earlier project of the author's (the
// offscreen device and text pipeline), drawing reel's scenes.

import (
	"flag"
	"fmt"
	"image"
	"image/png"
	"os"
	"time"

	"github.com/Zyko0/go-sdl3/sdl"

	"github.com/anthonyrego/reel/internal/draw"
	"github.com/anthonyrego/reel/internal/gfx"
	"github.com/anthonyrego/reel/internal/mac"
	"github.com/anthonyrego/reel/internal/platform"
	"github.com/anthonyrego/reel/internal/text"
	"github.com/anthonyrego/reel/internal/theme"
	"github.com/anthonyrego/reel/internal/ui"
)

func init() {
	register(&command{name: "render", summary: "draw a scene to a PNG (no window, no Dock icon)", run: runRender})
}

// offscreen is a device, renderer and text pipeline with no window.
type offscreen struct {
	device   *sdl.GPUDevice
	renderer *gfx.Renderer
	shaper   *text.Shaper
	glyphs   *text.GlyphCache
}

// newOffscreen initializes SDL in background mode (no Dock icon, no focus
// change) and creates the renderer. Must run on the main thread.
func newOffscreen(debug bool) (*offscreen, func(), error) {
	if err := platform.Init(platform.Background); err != nil {
		return nil, nil, err
	}
	device, err := gfx.NewDevice(debug)
	if err != nil {
		platform.Quit()
		return nil, nil, err
	}
	r, err := gfx.NewRenderer(device)
	if err != nil {
		device.Destroy()
		platform.Quit()
		return nil, nil, err
	}
	shaper, glyphs, err := newTextPipeline()
	if err != nil {
		r.Destroy()
		device.Destroy()
		platform.Quit()
		return nil, nil, err
	}
	r.SetGlyphCache(glyphs)
	o := &offscreen{device: device, renderer: r, shaper: shaper, glyphs: glyphs}
	return o, func() { r.Destroy(); device.Destroy(); platform.Quit() }, nil
}

// newTextPipeline returns the shaper and a glyph cache over the default
// rasterizer.
func newTextPipeline() (*text.Shaper, *text.GlyphCache, error) {
	// Load the system font index in the background, so the first line that
	// needs a fallback face doesn't wait for it.
	text.PreloadFonts()
	shaper, err := text.NewShaper()
	if err != nil {
		return nil, nil, err
	}
	rast, err := text.NewDefaultRasterizer()
	if err != nil {
		return nil, nil, err
	}
	return shaper, text.NewGlyphCache(rast), nil
}

// image draws one frame of a scene at w×h logical units and scale pixels
// per unit.
func (o *offscreen) image(s scene, w, h, scale float32, pal theme.Palette) (*image.RGBA, error) {
	c := &ui.Ctx{Text: o.shaper, Pal: pal, Scale: scale, Now: time.Now()}
	var l draw.List
	s.draw(c, &l, w, h)
	pw, ph := int(w*scale+0.5), int(h*scale+0.5)
	b := gfx.Batch{Glyphs: o.glyphs}
	b.Reset(pw, ph)
	b.AddList(&l, scale)
	return gfx.RenderImage(o.device, o.renderer, pw, ph, pal.Background, &b)
}

func runRender(args []string) error {
	fs := flag.NewFlagSet("render", flag.ContinueOnError)
	out := fs.String("out", "", "PNG to write (required)")
	width := fs.Float64("width", 760, "window width, logical units")
	height := fs.Float64("height", 560, "window height, logical units")
	scale := fs.Float64("scale", 2, "pixels per logical unit")
	dark := fs.Bool("dark", false, "use the dark palette")
	live := fs.Bool("live", false, "show this machine's permissions and devices (needs no permission; lists windows only with Screen Recording)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return fmt.Errorf("-out is required")
	}
	o, done, err := newOffscreen(false)
	if err != nil {
		return err
	}
	defer done()

	h := newHome(func() {})
	if *live {
		h.st = gatherHome()
	} else {
		h.st = sampleHome()
	}
	h.have = true
	img, err := o.image(h, float32(*width), float32(*height), float32(*scale), theme.For(*dark))
	if err != nil {
		return err
	}
	return writePNG(*out, img)
}

// sampleHome is a fixed state for renders: one permission missing, two
// displays, three mics.
func sampleHome() homeState {
	return homeState{
		screen: mac.PermissionGranted,
		mic:    mac.PermissionUnknown,
		content: mac.Content{
			Displays: []mac.Display{
				{ID: 1, Frame: mac.Rect{W: 1728, H: 1117}, Scale: 2},
				{ID: 2, Frame: mac.Rect{X: 1728, W: 3008, H: 1692}, Scale: 2},
			},
			Windows: make([]mac.Window, 14),
		},
		mics: []mac.Mic{
			{ID: "BuiltInMicrophoneDevice", Name: "MacBook Pro Microphone", Transport: mac.TransportBuiltIn, Default: true},
			{ID: "usb-1", Name: "Shure MV7", Transport: mac.TransportUSB},
			{ID: "bt-1", Name: "AirPods Pro", Transport: mac.TransportBluetooth},
		},
	}
}

func writePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
