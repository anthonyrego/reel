package main

// Trimming in the app: the main window shows the trim screen in place of
// the list. The picture at a handle comes from the recording through
// AVAssetImageGenerator off the main thread, one at a time: while one is
// being made, a newer request replaces any waiting one, so a drag shows
// the newest place it can. Saving writes the copy off the main thread,
// then copies it to the clipboard and goes back to the list.

import (
	"image"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/Zyko0/go-sdl3/sdl"

	"github.com/anthonyrego/reel/internal/draw"
	"github.com/anthonyrego/reel/internal/gfx"
	"github.com/anthonyrego/reel/internal/library"
	"github.com/anthonyrego/reel/internal/mac"
	"github.com/anthonyrego/reel/internal/mp4"
	"github.com/anthonyrego/reel/internal/ui"
	"github.com/anthonyrego/reel/internal/ui/trim"
)

// maxFrameEdge bounds the picture's long edge, in pixels.
const maxFrameEdge = 2560

type trimScene struct {
	a    *winApp
	c    *controller
	v    *trim.View
	path string
	back scene // what the main window showed before

	frames *mac.Frames // nil until open
	tex    *sdl.GPUTexture
	edge   int // the long edge to ask for, in pixels: the window's width

	want     time.Duration // the picture asked for last
	fetching bool          // one is being made
	waiting  bool          // and another was asked for since
	closed   bool
}

// openTrim shows the trim screen for the recording at path. Main thread.
func openTrim(a *winApp, c *controller, path string) {
	f, err := mp4.ReadFile(path)
	if err != nil {
		c.say("Couldn't read it: " + err.Error())
		return
	}
	v, _ := f.Track("vide")
	s := &trimScene{a: a, c: c, path: path, back: a.main.scene, edge: 1440}
	s.v = trim.New(trim.State{
		Name:     strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)),
		Duration: seconds(f.Duration),
		Width:    v.Width, Height: v.Height,
	})
	s.v.Act = trim.Actions{Show: s.show, Copy: s.copy, Replace: s.replace, Cancel: s.close}
	a.setScene(s)
	go func() {
		fr, err := mac.OpenFrames(path)
		c.post(func() {
			switch {
			case err != nil:
				s.v.St.Error = "Couldn't read the picture: " + err.Error()
			case s.closed:
				go fr.Close()
			default:
				s.frames = fr
				s.show(s.want)
			}
		})
	}()
}

// show asks for the picture at t.
func (s *trimScene) show(t time.Duration) {
	s.want = t
	switch {
	case s.frames == nil || s.closed:
	case s.fetching:
		s.waiting = true
	default:
		s.fetch()
	}
}

func (s *trimScene) fetch() {
	s.fetching, s.waiting = true, false
	fr, t, edge := s.frames, s.want, s.edge
	go func() {
		img, at, err := fr.At(t, edge)
		s.c.post(func() { s.got(img, at, err) })
	}()
}

// got takes a picture that was made. Main thread.
func (s *trimScene) got(img *image.RGBA, at time.Duration, err error) {
	s.fetching = false
	if s.closed {
		return
	}
	if err != nil {
		slog.Warn("trim: picture", "at", s.want, "err", err)
	} else if tex, err := gfx.UploadTexture(s.a.device, sdl.GPU_TEXTUREFORMAT_R8G8B8A8_UNORM,
		uint32(img.Bounds().Dx()), uint32(img.Bounds().Dy()), img.Pix); err != nil {
		slog.Warn("trim: upload the picture", "err", err)
	} else {
		s.a.renderer.SetImage(trimImage, tex)
		if s.tex != nil {
			s.a.device.ReleaseTexture(s.tex)
		} else {
			slog.Info("trim: first picture", "path", s.path, "at", at, "size", img.Bounds().Size())
		}
		s.tex = tex
		s.v.St.Frame, s.v.St.Shown = trimImage, at
	}
	if s.waiting {
		s.fetch()
	}
}

func (s *trimScene) copy(in, out time.Duration)    { s.save(in, out, false) }
func (s *trimScene) replace(in, out time.Duration) { s.save(in, out, true) }

// save writes the trim off the main thread.
func (s *trimScene) save(in, out time.Duration, replace bool) {
	s.v.St.Busy, s.v.St.Error = "Saving…", ""
	src := s.path
	go func() {
		start := time.Now()
		var dst string
		var err error
		if replace {
			dst, err = trimReplace(src, in, out, mac.Trash)
		} else {
			dst = library.TrimmedPath(src)
			if err = trimTo(src, dst, in, out); err != nil {
				dst = ""
			}
		}
		slog.Info("app: trim", "src", src, "dst", dst, "from", in, "to", out, "replace", replace, "took", time.Since(start), "err", err)
		s.c.post(func() { s.saved(dst, replace, err) })
	}()
}

// saved takes a finished save: the trim goes on the clipboard and the
// list comes back. Main thread.
func (s *trimScene) saved(dst string, replace bool, err error) {
	s.v.St.Busy = ""
	if dst == "" {
		s.v.St.Error = "Couldn't save the trim: " + err.Error()
		return
	}
	done := "Trimmed"
	switch {
	case err != nil: // replacing, the original wouldn't go to the Trash (logged)
		done = "Saved as a copy (the original wouldn't go to the Trash)"
	case replace:
		done = "Replaced"
	}
	if cerr := mac.CopyFile(dst); cerr != nil {
		s.c.say(done + " (couldn't copy it: " + cerr.Error() + ")")
	} else {
		s.c.copied = dst
		s.c.say(done + ": copied, paste it into Slack")
	}
	s.c.refresh()
	s.close()
}

// close goes back to the list.
func (s *trimScene) close() {
	if s.closed {
		return
	}
	s.closed = true
	if fr := s.frames; fr != nil {
		go fr.Close() // waits for a picture being made
	}
	s.a.renderer.SetImage(trimImage, nil)
	if s.tex != nil {
		s.a.device.ReleaseTexture(s.tex)
		s.tex = nil
	}
	setCursor(ui.CursorDefault)
	s.a.setScene(s.back)
	s.c.show()
}

func (s *trimScene) draw(c *ui.Ctx, l *draw.List, w, h float32) {
	s.edge = min(int(w*c.Scale), maxFrameEdge)
	s.v.Draw(c, l, w, h)
}

func (s *trimScene) event(c *ui.Ctx, ev ui.Event) bool {
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

func (s *trimScene) animating() bool { return false }

// woke runs what the controller was posted (the pictures, the save), as
// the list's scene does when it's showing.
func (s *trimScene) woke() { s.c.woke() }
