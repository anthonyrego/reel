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
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Zyko0/go-sdl3/sdl"

	"github.com/anthonyrego/reel/internal/config"
	"github.com/anthonyrego/reel/internal/draw"
	"github.com/anthonyrego/reel/internal/gfx"
	"github.com/anthonyrego/reel/internal/mac"
	"github.com/anthonyrego/reel/internal/mp4"
	"github.com/anthonyrego/reel/internal/platform"
	"github.com/anthonyrego/reel/internal/text"
	"github.com/anthonyrego/reel/internal/theme"
	"github.com/anthonyrego/reel/internal/thumb"
	"github.com/anthonyrego/reel/internal/ui"
	"github.com/anthonyrego/reel/internal/ui/controls"
	"github.com/anthonyrego/reel/internal/ui/home"
	"github.com/anthonyrego/reel/internal/ui/icons"
	"github.com/anthonyrego/reel/internal/ui/player"
	"github.com/anthonyrego/reel/internal/ui/trim"
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
func (o *offscreen) image(s scene, w, h, scale float32, pal theme.Palette, now time.Time) (*image.RGBA, error) {
	c := &ui.Ctx{Text: o.shaper, Pal: pal, Scale: scale, Now: now}
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
	width := fs.Float64("width", 800, "window width, logical units")
	height := fs.Float64("height", 640, "window height, logical units")
	scale := fs.Float64("scale", 2, "pixels per logical unit")
	state := fs.String("state", "idle", "the main window's sample state: "+sampleNames())
	hover := fs.String("hover", "", "move the pointer here first (a tooltip shows): X,Y in logical units")
	sceneName := fs.String("scene", "home", "what to draw: home (the main window), hud (the floating controls, at -state live or paused), area (the area picker over a made-up desktop), icons (every icon at three sizes), bubble (the camera bubble, at -state "+strings.Join(bubbleStates, ", ")+") trim (the trim screen, at -state "+strings.Join(trimStates, ", ")+") or player (the player screen, at -state "+strings.Join(playerStates, ", ")+")")
	file := fs.String("file", "", "with -scene trim or player: show this recording's picture (default: a made-up one)")
	live := fs.Bool("live", false, "draw this Mac's real state (permissions, displays, mics, recordings) through the controller, menus closed")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return fmt.Errorf("-out is required")
	}
	sample, ok := samples[*state]
	if !ok && *sceneName != "trim" && *sceneName != "bubble" && *sceneName != "player" {
		return fmt.Errorf("-state %q: one of %s", *state, sampleNames())
	}
	if !ok {
		sample = samples["idle"]
	}
	o, done, err := newOffscreen(false)
	if err != nil {
		return err
	}
	defer done()

	now := time.Now()
	st, menu := sample(now)
	v := home.New()
	v.St, v.Act = st, sampleActions()
	if *live {
		paths, err := config.Resolve()
		if err != nil {
			return err
		}
		c := newController(paths, v)
		c.refresh()
		for deadline := time.Now().Add(10 * time.Second); c.gathering && time.Now().Before(deadline); {
			time.Sleep(50 * time.Millisecond)
			c.woke() // runs what the refresh posted
		}
		c.show()
		menu = ""
	}
	if *sceneName == "home" {
		// The recordings' pictures: made up, or this Mac's (-live).
		if *live {
			defer liveThumbs(o, v)()
		} else {
			for i := range v.St.Recordings {
				id := thumbBase + draw.ImageID(i)
				tex, err := samplePicture(o, id, 600, 375, i+1)
				if err != nil {
					return err
				}
				defer o.device.ReleaseTexture(tex)
				v.St.Recordings[i].Thumb = id
			}
		}
	}
	pal := theme.Dark
	w, h := float32(*width), float32(*height)
	c := &ui.Ctx{Text: o.shaper, Pal: pal, Scale: float32(*scale), Now: now}
	// Lay it out once, so the pointer below finds the controls.
	var scratch draw.List
	v.Draw(c, &scratch, w, h)
	v.OpenMenu(menu)
	at, pointing := draw.Point{}, false
	switch {
	case *hover != "":
		if _, err := fmt.Sscanf(*hover, "%f,%f", &at.X, &at.Y); err != nil {
			return fmt.Errorf("-hover %q: want X,Y", *hover)
		}
		pointing = true
	case *state == "hover-card" && len(v.St.Recordings) > 0:
		// On the first card's Trim, its tooltip showing.
		card, _ := v.Card(v.St.Recordings[0].Path)
		at, pointing = draw.Point{X: card.Right() - 8 - 2*34 - 22, Y: card.Y + 8 + 14}, true
	case *state == "hover-play" && len(v.St.Recordings) > 0:
		// On the first card's Play, its tooltip showing.
		card, _ := v.Card(v.St.Recordings[0].Path)
		picH := card.H - 8 - 40
		at, pointing = draw.Point{X: card.X + card.W/2, Y: card.Y + picH/2}, true
	case *state == "delete" && len(v.St.Recordings) > 0:
		v.AskDelete(v.St.Recordings[0].Path)
	}
	if pointing {
		v.Event(c, ui.Event{Kind: ui.PointerMove, Pos: at})
		v.Draw(c, &scratch, w, h) // the tooltip's wait starts now
		now = now.Add(time.Second)
	}
	var sc scene = &homeScene{v: v}
	switch *sceneName {
	case "hud":
		c := &controller{v: v}
		sc, w, h = &hud{c: c, hits: controls.NewHits()}, hudW, hudH
		pal.Background = draw.RGB(0x6a7f9a) // a desktop to float over
	case "trim":
		ts, release, err := sampleTrim(o, *file, *state, w, h, float32(*scale))
		if err != nil {
			return err
		}
		defer release()
		sc = ts
	case "player":
		ps, release, err := samplePlayer(o, *file, *state, w, h, float32(*scale))
		if err != nil {
			return err
		}
		defer release()
		sc = ps
	case "area":
		tex, err := sampleDesktop(o, areaImage, int(w*float32(*scale)), int(h*float32(*scale)))
		if err != nil {
			return err
		}
		defer o.device.ReleaseTexture(tex)
		as := &areaScene{hits: controls.NewHits()}
		if *hover != "" {
			as.have, as.from, as.to = true, draw.Point{X: w * 0.22, Y: h * 0.25}, draw.Point{X: w * 0.7, Y: h * 0.68}
		}
		sc = as
	case "icons":
		sc = iconSheet{}
	case "bubble":
		look := *state
		if look == "idle" {
			look = "picture"
		}
		face, crop, ok := bubbleSampleFace(look)
		if !ok {
			return fmt.Errorf("-scene bubble -state %q: %s", look, strings.Join(bubbleStates, ", "))
		}
		side := float32(bubbleDefault)
		w, h = side+2*bubblePad, side+2*bubblePad
		if face.pic {
			tex, err := sampleCamera(o, int(side*float32(*scale)), crop)
			if err != nil {
				return err
			}
			defer o.device.ReleaseTexture(tex)
		}
		sc = &bubbleSample{face: face}
		pal.Background = draw.RGB(0x6a7f9a) // a desktop to float over
	case "home":
	default:
		return fmt.Errorf("-scene %q: home, hud, area, trim, player, bubble or icons", *sceneName)
	}
	img, err := o.image(sc, w, h, float32(*scale), pal, now)
	if err != nil {
		return err
	}
	return writePNG(*out, img)
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

// sampleDesktop registers a made-up desktop (a gradient with a few
// window-like cards) as image id: the area picker's still, a recording's
// picture.
func sampleDesktop(o *offscreen, id draw.ImageID, pw, ph int) (*sdl.GPUTexture, error) {
	return samplePicture(o, id, pw, ph, 0)
}

// samplePicture is sampleDesktop in one of a few looks (seed), for the
// recordings' cards.
func samplePicture(o *offscreen, id draw.ImageID, pw, ph, seed int) (*sdl.GPUTexture, error) {
	tex, err := gfx.UploadTexture(o.device, sdl.GPU_TEXTUREFORMAT_R8G8B8A8_UNORM, uint32(pw), uint32(ph), samplePixels(pw, ph, seed).Pix)
	if err != nil {
		return nil, err
	}
	o.renderer.SetImage(id, tex)
	return tex, nil
}

// samplePixels is a made-up desktop pw×ph in one of a few looks.
func samplePixels(pw, ph, seed int) *image.RGBA {
	bases := [][3]int{{40, 70, 150}, {110, 50, 90}, {30, 100, 100}, {140, 90, 40}, {70, 50, 140}, {50, 60, 70}}
	layouts := [][][4]int{
		{{pw / 10, ph / 8, pw / 2, ph / 2}, {pw / 2, ph / 3, pw * 9 / 10, ph * 5 / 6}},
		{{pw / 8, ph / 10, pw * 7 / 8, ph * 9 / 10}},
		{{pw / 14, ph / 6, pw * 13 / 28, ph * 5 / 6}, {pw * 15 / 28, ph / 6, pw * 13 / 14, ph * 5 / 6}},
		{{pw / 5, ph / 5, pw * 4 / 5, ph * 3 / 4}},
		{{pw / 12, ph / 12, pw * 2 / 3, ph * 2 / 3}, {pw / 3, ph / 2, pw * 11 / 12, ph * 11 / 12}},
		{{pw / 4, ph / 8, pw * 3 / 4, ph * 7 / 8}},
	}
	base, cards := bases[seed%len(bases)], layouts[seed%len(layouts)]
	pix := make([]byte, pw*ph*4)
	for y := range ph {
		for x := range pw {
			i := (y*pw + x) * 4
			pix[i], pix[i+1], pix[i+2], pix[i+3] = byte(base[0]+60*x/pw), byte(base[1]+80*y/ph), byte(base[2]+60*x/pw), 255
			for _, r := range cards {
				if x >= r[0] && x < r[2] && y >= r[1] && y < r[3] {
					pix[i], pix[i+1], pix[i+2] = 245, 245, 247
					if y < r[1]+ph/30 {
						pix[i], pix[i+1], pix[i+2] = 225, 225, 230
					}
				}
			}
		}
	}
	return &image.RGBA{Pix: pix, Stride: 4 * pw, Rect: image.Rect(0, 0, pw, ph)}
}

// trimStates are the trim screen's sample states.
var trimStates = []string{"idle", "uncut", "replace", "saving", "loading", "error"}

// sampleTrim is the trim screen w×h at scale in a sample state, over a
// made-up picture and filmstrip or, with file, that recording's.
func sampleTrim(o *offscreen, file, state string, w, h, scale float32) (scene, func(), error) {
	edge := int(w * scale)
	if !slices.Contains(trimStates, state) {
		return nil, nil, fmt.Errorf("-state %q: with -scene trim, one of %s", state, strings.Join(trimStates, ", "))
	}
	st := trim.State{Name: "Recording 2026-10-13 at 15.04.05", Title: "3:04 PM", Duration: 47300 * time.Millisecond, Width: 1600, Height: 1000}
	in, out := 4200*time.Millisecond, 31800*time.Millisecond
	var img *image.RGBA
	if file != "" {
		f, err := mp4.ReadFile(file)
		if err != nil {
			return nil, nil, err
		}
		v, _ := f.Track("vide")
		st.Name = strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
		st.Title = ""
		st.Duration, st.Width, st.Height = seconds(f.Duration), v.Width, v.Height
		in, out = st.Duration/5, st.Duration*3/4
		fr, err := mac.OpenFrames(file)
		if err != nil {
			return nil, nil, err
		}
		defer fr.Close()
		if img, st.Shown, err = fr.At(in, edge); err != nil {
			return nil, nil, err
		}
	}
	v := trim.New(st)
	// Actions that do nothing, so every control is drawn enabled.
	v.Act = trim.Actions{Show: func(time.Duration) {}, Copy: func(_, _ time.Duration) {}, Replace: func(_, _ time.Duration) {}, Cancel: func() {}}
	v.SetRange(in, out)
	switch state {
	case "uncut":
		v.SetRange(0, st.Duration)
		in = 0
	case "replace":
		v.AskReplace()
	case "saving":
		v.St.Busy = "Saving…"
	case "error":
		v.St.Error = "Couldn't save the trim: the disk is full"
	}
	// The filmstrip, for the track as it's laid out.
	var strip *sdl.GPUTexture
	if state != "loading" {
		var l draw.List
		v.Draw(&ui.Ctx{Text: o.shaper, Pal: theme.Dark, Scale: scale, Now: time.Now()}, &l, w, h)
		pw, ph := v.TrackPixels(scale)
		aspect := st.Width / st.Height
		tiles := make([]*image.RGBA, stripTiles)
		if file != "" {
			readStrip(file, st.Duration, stripEdge(scale, aspect), nil, func(k int, img *image.RGBA) { tiles[k] = img })
		} else {
			for k := range tiles {
				tiles[k] = samplePixels(int(float64(ph)*aspect), ph, k/3)
			}
		}
		img := thumb.Strip(tiles, int(float64(ph)*aspect+0.5), ph, pw)
		var err error
		if strip, err = gfx.UploadTexture(o.device, sdl.GPU_TEXTUREFORMAT_R8G8B8A8_UNORM, uint32(pw), uint32(ph), img.Pix); err != nil {
			return nil, nil, err
		}
		o.renderer.SetImage(trimStrip, strip)
		v.St.Strip = trimStrip
	}
	release := func() {
		if strip != nil {
			o.renderer.SetImage(trimStrip, nil)
			o.device.ReleaseTexture(strip)
		}
	}
	if state != "loading" {
		var tex *sdl.GPUTexture
		var err error
		if img != nil {
			tex, err = gfx.UploadTexture(o.device, sdl.GPU_TEXTUREFORMAT_R8G8B8A8_UNORM, uint32(img.Bounds().Dx()), uint32(img.Bounds().Dy()), img.Pix)
			o.renderer.SetImage(trimImage, tex)
		} else {
			tex, err = sampleDesktop(o, trimImage, 1600, 1000)
			v.St.Shown = in
		}
		if err != nil {
			return nil, nil, err
		}
		v.St.Frame = trimImage
		releaseStrip := release
		release = func() { releaseStrip(); o.renderer.SetImage(trimImage, nil); o.device.ReleaseTexture(tex) }
	}
	return &trimScene{v: v}, release, nil
}

// playerStates are the player screen's sample states.
var playerStates = []string{"idle", "paused", "scrub", "muted", "loading", "error", "copy-error"}

// samplePlayer is the player screen w×h at scale in a sample state, over a
// made-up picture or, with file, that recording's frame a quarter in,
// taken the way the app takes it (mac.Player, BGRA, at the picture's size).
func samplePlayer(o *offscreen, file, state string, w, h, scale float32) (scene, func(), error) {
	if !slices.Contains(playerStates, state) {
		return nil, nil, fmt.Errorf("-state %q: with -scene player, one of %s", state, strings.Join(playerStates, ", "))
	}
	st := player.State{Name: "Recording 2026-10-13 at 15.04.05", Title: "3:04 PM", Duration: 47300 * time.Millisecond, Width: 1600, Height: 1000,
		Time: 12400 * time.Millisecond, Playing: true}
	var native [2]int
	if file != "" {
		f, err := mp4.ReadFile(file)
		if err != nil {
			return nil, nil, err
		}
		v, _ := f.Track("vide")
		st.Name = strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
		st.Title = ""
		st.Duration, st.Width, st.Height = seconds(f.Duration), v.Width, v.Height
		st.Time = st.Duration / 4
		native = [2]int{int(v.Width), int(v.Height)}
	}
	v := player.New(st)
	// Actions that do nothing, so every control is drawn enabled.
	nop := func() {}
	v.Act = player.Actions{Play: nop, Pause: nop, Seek: func(time.Duration, bool) {}, SetMuted: func(bool) {}, Copy: nop, Trim: nop, Reveal: nop, Back: nop}
	ctx := &ui.Ctx{Text: o.shaper, Pal: theme.Dark, Scale: scale, Now: time.Now()}
	var l draw.List
	v.Draw(ctx, &l, w, h)
	switch state {
	case "paused":
		v.St.Playing = false
	case "scrub":
		v.St.Playing = false
		bar := v.Bar()
		v.Hover(draw.Point{X: bar.X + bar.W*0.62, Y: bar.Y + bar.H/2})
	case "muted":
		v.St.Muted, v.St.Copied = true, true
	case "error":
		v.St.Error = "Couldn't play it: The file couldn't be opened."
	case "copy-error":
		v.St.Playing, v.St.Error = false, "Couldn't copy it: the pasteboard refused it"
	}
	ps := &playScene{v: v, device: o.device, renderer: o.renderer, native: native}
	if state == "loading" || state == "error" {
		return ps, func() {}, nil
	}
	if file == "" {
		tex, err := samplePicture(o, playerImage, 1600, 1000, 1)
		if err != nil {
			return nil, nil, err
		}
		v.St.Frame = playerImage
		return ps, func() { o.renderer.SetImage(playerImage, nil); o.device.ReleaseTexture(tex) }, nil
	}
	if err := sampleFrame(ps, file, st.Time, scale); err != nil {
		return nil, nil, err
	}
	v.St.Frame = playerImage
	return ps, ps.release, nil
}

// sampleFrame puts the frame at t of the recording at path in ps's
// texture, as the app would show it: mac.Player's, at the picture's size.
// SDL's events are pumped meanwhile, which runs the main run loop, where
// AVPlayer says what it's doing.
func sampleFrame(ps *playScene, path string, t time.Duration, scale float32) error {
	p, err := mac.OpenPlayer(path)
	if err != nil {
		return err
	}
	defer p.Close()
	ps.p = p
	defer func() { ps.p = nil }() // drawn as a render: nothing to sync
	p.SetMuted(true)
	ps.size(ps.v.Picture(), scale)
	wait := func(what string, done func() (bool, error)) error {
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			sdl.PumpEvents()
			if ok, err := done(); ok || err != nil {
				return err
			}
		}
		return fmt.Errorf("render: %s took over 5 s", what)
	}
	if err := wait("opening it", p.Ready); err != nil {
		return err
	}
	p.Seek(t, true)
	var upErr error
	return wait("its frame", func() (bool, error) {
		_, ok := p.Frame(mac.HostTime().Seconds(), func(pix []byte, w, h, stride int) { upErr = ps.upload(pix, w, h, stride) })
		return ok, upErr
	})
}

// iconSheet draws every icon at three sizes, with its name.
type iconSheet struct{}

// bubbleStates are the bubble's sample states.
var bubbleStates = []string{"picture", "placeholder", "off", "hover", "option", "square", "framed"}

// bubbleSampleFace is what the bubble draws at a sample state, and the
// part of the fake camera's picture it shows: under the pointer (hover:
// the handles and the help; option: ⌥ held), a square with its handles on
// its corners, and one framed on the fake picture's marker as a recording
// would show it.
func bubbleSampleFace(state string) (bubbleFace, thumb.Crop, bool) {
	f := bubbleFace{pic: true, round: 1}
	var crop thumb.Crop
	switch state {
	case "picture":
	case "placeholder":
		f.pic = false
	case "off":
		f.pic, f.failed = false, true
	case "hover":
		f.handles, f.hint = true, true
	case "option":
		f.framing, f.hint = true, true
	case "square":
		f.round, f.handles, f.hint, f.hot = 0, true, true, partSize
	case "framed":
		f.round, crop = 0.35, thumb.Crop{Zoom: 1.8, X: 0.155, Y: -0.2}
	default:
		return f, crop, false
	}
	return f, crop, true
}

// bubbleSample is the camera bubble drawn alone.
type bubbleSample struct {
	face  bubbleFace
	texts controls.Texts
}

func (b *bubbleSample) draw(c *ui.Ctx, l *draw.List, w, h float32) {
	drawBubble(c, l, &b.texts, w, h, b.face)
}
func (*bubbleSample) event(*ui.Ctx, ui.Event) bool { return false }
func (*bubbleSample) animating() bool              { return false }
func (*bubbleSample) woke()                        {}

// sampleCamera registers the fake camera's picture, cut as the bubble
// cuts a frame (side pixels, crop's part), as the bubble's image.
func sampleCamera(o *offscreen, side int, crop thumb.Crop) (*sdl.GPUTexture, error) {
	pix := make([]byte, fakeCamW*fakeCamH*4)
	fakePicture(pix, 40, false)
	img := image.NewRGBA(image.Rect(0, 0, side, side))
	var cut thumb.Bubble
	cut.Cut(img, pix, fakeCamW, fakeCamH, fakeCamW*4, crop)
	tex, err := gfx.UploadTexture(o.device, sdl.GPU_TEXTUREFORMAT_R8G8B8A8_UNORM, uint32(side), uint32(side), img.Pix)
	if err != nil {
		return nil, err
	}
	o.renderer.SetImage(cameraImage, tex)
	return tex, nil
}

func (iconSheet) draw(c *ui.Ctx, l *draw.List, w, h float32) {
	names := slices.Sorted(maps.Keys(icons.All))
	t := &controls.Texts{}
	for i, name := range names {
		x, y := float32(24+(i%4)*170), float32(24+(i/4)*56)
		for j, size := range []float32{14, 18, 28} {
			icons.All[name](l, x+float32(j)*36+14, y+14, size, c.Pal.Text)
		}
		controls.Put(l, t.Lay(c, name, text.SansRegular, 11), x, y+34, c.Pal.TextMuted)
	}
}
func (iconSheet) event(*ui.Ctx, ui.Event) bool { return false }
func (iconSheet) animating() bool              { return false }
func (iconSheet) woke()                        {}
