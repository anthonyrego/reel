package main

// Playing a recording in the app: the main window shows the player screen
// in place of the list. mac.Player plays the sound and keeps the clock; at
// each of the window's frames while it plays, the scene takes the frame
// for when that frame will be on screen (aheadRefreshes from now), scaled to
// the picture's size in pixels, and uploads it, so the picture follows the
// sound. Paused, it draws only when something changes, and at the
// display's pace while a frame it wants (the first, a seek's) is coming.

import (
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/Zyko0/go-sdl3/sdl"

	"github.com/anthonyrego/reel/internal/draw"
	"github.com/anthonyrego/reel/internal/gfx"
	"github.com/anthonyrego/reel/internal/mac"
	"github.com/anthonyrego/reel/internal/mp4"
	"github.com/anthonyrego/reel/internal/ui"
	"github.com/anthonyrego/reel/internal/ui/home"
	"github.com/anthonyrego/reel/internal/ui/player"
)

// frameWait bounds how long a paused player draws at the display's pace
// for a frame it wants.
const frameWait = 2 * time.Second

// aheadRefreshes is how many of the display's refreshes after a frame is
// drawn it's on screen: the frame taken is the one for then, so the
// picture meets the sound AVPlayer plays. Drawn after a vsync, a frame is
// presented at the next and composited at the one after; the self-test's
// beacon is captured 31 ms (1.9 refreshes at 60 Hz) after it's drawn.
// Not measured for the player.
const aheadRefreshes = 2

// endSlack is how near the end a stop counts as the end: Play then starts
// again from the start.
const endSlack = 50 * time.Millisecond

type playScene struct {
	a        *winApp
	c        *controller
	device   *sdl.GPUDevice // the picture's texture's (a's, or a render's)
	renderer *gfx.Renderer
	v        *player.View
	path     string
	back     scene // what the main window showed before

	p          *mac.Player // nil in renders
	ahead      float64     // aheadRefreshes in seconds: when a frame drawn now is on screen
	native     [2]int      // the recording's frame size: frames come no larger
	out        [2]int      // the frames' size asked for
	tex        *sdl.GPUTexture
	texW, texH int
	up         *gfx.Uploader
	ready      bool
	// want is since when a frame is wanted while paused (zero: none);
	// seekTo, while set, is where a seek went, shown until its frame
	// comes (the player's clock may lag it).
	want   time.Time
	seekTo *time.Duration
	closed bool

	// How it went, for the log: frames taken, the window's frames drawn
	// while it played, and the time spent uploading.
	opened time.Time
	frames int
	draws  int
	took   time.Duration
}

// openPlayer shows the player screen for the recording at path, playing
// from the start. Main thread.
func openPlayer(a *winApp, c *controller, path string) {
	f, err := mp4.ReadFile(path)
	if err != nil {
		c.say("Couldn't read it: " + err.Error())
		return
	}
	p, err := mac.OpenPlayer(path)
	if err != nil {
		c.say("Couldn't play it: " + err.Error())
		return
	}
	v, _ := f.Track("vide")
	now := time.Now()
	s := &playScene{a: a, c: c, device: a.device, renderer: a.renderer, path: path, back: a.main.scene, p: p,
		ahead:  aheadRefreshes * displayRefresh(a.window.Handle()),
		native: [2]int{int(v.Width), int(v.Height)}, want: now, opened: now}
	s.v = player.New(player.State{
		Name:     strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)),
		Title:    c.title(path),
		Duration: seconds(f.Duration),
		Width:    v.Width, Height: v.Height,
		Muted:  c.muted,
		Copied: c.copied == path,
	})
	s.v.Act = player.Actions{
		Play: s.play, Pause: s.pause, Seek: s.seek, SetMuted: s.setMuted,
		Copy: s.copy, Trim: s.trim, Reveal: func() { c.reveal(path) }, Back: s.close,
	}
	p.SetMuted(c.muted)
	p.Play()
	a.setScene(s)
	slog.Info("player: open", "path", path, "size", s.native, "duration", s.v.St.Duration)
}

// title is what the screen calls the recording at path: as its card does.
func (c *controller) title(path string) string {
	for _, r := range c.recs {
		if r.Path == path {
			return home.Title(home.Recording{Name: r.Name, When: r.When}, time.Now())
		}
	}
	return ""
}

func (s *playScene) play() {
	if st := &s.v.St; st.Time >= st.Duration-endSlack {
		s.seek(0, true) // at the end: from the start
	}
	s.p.Play()
	s.v.St.Playing = true
}

func (s *playScene) pause() {
	s.p.Pause()
	s.v.St.Playing = false
}

func (s *playScene) seek(t time.Duration, exact bool) {
	s.p.Seek(t, exact)
	s.v.St.Time, s.seekTo, s.want = t, &t, time.Now()
}

func (s *playScene) setMuted(m bool) {
	s.p.SetMuted(m)
	s.v.St.Muted, s.c.muted = m, m
}

func (s *playScene) copy() {
	if err := mac.CopyFile(s.path); err != nil {
		s.v.St.Error = "Couldn't copy it: " + err.Error()
		return
	}
	s.c.copied = s.path
	s.v.St.Copied, s.v.St.Error = true, ""
}

// trim opens the trim screen on the recording, which goes back to the
// list.
func (s *playScene) trim() {
	s.close()
	if s.c.openTrim != nil {
		s.c.openTrim(s.path)
	}
}

// close goes back to the list.
func (s *playScene) close() {
	if s.closed {
		return
	}
	s.closed = true
	var each time.Duration
	if s.frames > 0 {
		each = s.took / time.Duration(s.frames)
	}
	slog.Info("player: close", "path", s.path, "at", s.v.St.Time, "frames", s.frames, "draws", s.draws, "upload", each,
		"size", [2]int{s.texW, s.texH}, "open", time.Since(s.opened).Round(time.Millisecond))
	s.p.Close()
	s.release()
	setCursor(ui.CursorDefault)
	s.a.setScene(s.back)
	s.c.show()
}

// sync takes what the player is doing into the view's State.
func (s *playScene) sync() {
	st := &s.v.St
	ready, err := s.p.Ready()
	switch {
	case err != nil:
		if st.Error == "" {
			slog.Warn("player: can't play", "path", s.path, "err", err)
		}
		st.Error, st.Playing, s.want = "Couldn't play it: "+err.Error(), false, time.Time{}
		return
	case ready && !s.ready:
		s.ready = true
		if d := s.p.Duration(); d > 0 {
			st.Duration = d
		}
	}
	playing := s.p.Playing()
	if s.seekTo != nil && (s.want.IsZero() || time.Since(s.want) > frameWait) {
		s.seekTo = nil // its frame is the one shown
	}
	if s.seekTo != nil {
		st.Time = *s.seekTo
	} else if !s.v.Dragging() {
		st.Time = s.p.Time()
	}
	st.Playing = playing
}

// take uploads the frame for when this one will be on screen, if it's
// new. Main thread.
func (s *playScene) take() {
	start := time.Now()
	at, ok := s.p.Frame(mac.HostTime().Seconds()+s.ahead, func(pix []byte, w, h, stride int) {
		if err := s.upload(pix, w, h, stride); err != nil {
			slog.Error("player: upload a frame", "err", err)
		}
	})
	if !ok || s.tex == nil {
		return
	}
	s.took += time.Since(start)
	if s.frames == 0 {
		slog.Info("player: first frame", "at", at, "size", [2]int{s.texW, s.texH}, "after", time.Since(s.opened).Round(time.Millisecond))
	}
	s.frames++
	s.v.St.Frame = playerImage
	s.want, s.seekTo = time.Time{}, nil
}

// upload puts a frame (BGRA, w×h, stride bytes a row) in the picture's
// texture, made again for a new size.
func (s *playScene) upload(pix []byte, w, h, stride int) error {
	if s.tex == nil || s.texW != w || s.texH != h {
		if s.tex != nil {
			s.renderer.SetImage(playerImage, nil)
			s.device.ReleaseTexture(s.tex)
			s.tex = nil
		}
		tex, err := gfx.NewTexture(s.device, sdl.GPU_TEXTUREFORMAT_B8G8R8A8_UNORM, uint32(w), uint32(h))
		if err != nil {
			return err
		}
		s.tex, s.texW, s.texH = tex, w, h
	}
	if s.up == nil {
		s.up = gfx.NewUploader(s.device)
	}
	if err := s.up.Upload(s.tex, 0, 0, uint32(w), uint32(h), uint32(stride/4), pix); err != nil {
		return err
	}
	s.renderer.SetImage(playerImage, s.tex)
	return nil
}

// size asks for frames the size of the picture as drawn at scale, no
// larger than the recording's. A new size starts a new output, so it's
// asked for again only when it's an eighth off.
func (s *playScene) size(pic draw.Rect, scale float32) {
	w, h := int(pic.W*scale+0.5), int(pic.H*scale+0.5)
	if nw, nh := s.native[0], s.native[1]; nw > 0 && nh > 0 && w > nw {
		w, h = nw, nh
	}
	if w <= 0 || h <= 0 {
		return
	}
	if ow := s.out[0]; ow > 0 && abs(w-ow) <= ow/8 {
		return
	}
	s.out = [2]int{w, h}
	s.p.SetSize(w, h)
	s.want = time.Now() // the new output's first frame
}

func abs(n int) int { return max(n, -n) }

// release lets the picture's texture go.
func (s *playScene) release() {
	s.renderer.SetImage(playerImage, nil)
	if s.tex != nil {
		s.device.ReleaseTexture(s.tex)
		s.tex = nil
	}
	if s.up != nil {
		s.up.Release()
		s.up = nil
	}
}

func (s *playScene) draw(c *ui.Ctx, l *draw.List, w, h float32) {
	if s.p != nil && !s.closed {
		s.sync()
		if s.v.St.Playing {
			s.draws++
		}
		s.take()
	}
	s.v.Draw(c, l, w, h)
	if s.p != nil && !s.closed {
		s.size(s.v.Picture(), c.Scale)
	}
}

func (s *playScene) event(c *ui.Ctx, ev ui.Event) bool {
	redraw, hand := s.v.Event(c, ev)
	if ev.Kind == ui.PointerMove || ev.Kind == ui.PointerLeave {
		if hand {
			setCursor(ui.CursorPointer)
		} else {
			setCursor(ui.CursorDefault)
		}
	}
	return redraw
}

// animating: while it plays, or a frame it wants is coming.
func (s *playScene) animating() bool {
	if s.p == nil || s.closed {
		return false
	}
	return s.v.St.Playing || s.p.Playing() || (!s.want.IsZero() && time.Since(s.want) < frameWait)
}

// woke runs what the controller was posted, as the list's scene does
// when it's showing.
func (s *playScene) woke() { s.c.woke() }
