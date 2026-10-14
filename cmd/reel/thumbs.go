package main

// The recordings' pictures on their cards: each made off the main thread,
// one at a time (the cards on screen first), from a frame of the
// recording cut to a card's shape, and kept as a texture while it's
// wanted; the least recently drawn go first past maxThumbs.

import (
	"image"
	"log/slog"
	"math"
	"slices"
	"time"

	"github.com/Zyko0/go-sdl3/sdl"

	"github.com/anthonyrego/reel/internal/draw"
	"github.com/anthonyrego/reel/internal/gfx"
	"github.com/anthonyrego/reel/internal/library"
	"github.com/anthonyrego/reel/internal/mac"
	"github.com/anthonyrego/reel/internal/mp4"
	"github.com/anthonyrego/reel/internal/thumb"
	"github.com/anthonyrego/reel/internal/ui/home"
)

const (
	// thumbW×thumbH is a card's picture, in pixels (16:10, a wide card at
	// 2×).
	thumbW, thumbH = 600, 375
	maxThumbs      = 64
)

// thumbKey is a recording as it was when its picture was made: one
// replaced in place (a trim's Replace) is made again.
type thumbKey struct {
	path string
	size int64
	when time.Time
}

func keyOf(r library.Recording) thumbKey { return thumbKey{r.Path, r.Size, r.When} }

type thumbTex struct {
	id   draw.ImageID
	tex  *sdl.GPUTexture
	used int // the draw it was last wanted in
}

// thumbs owns the pictures' textures. Main thread, but for the work it
// hands its goroutine.
type thumbs struct {
	device   *sdl.GPUDevice
	renderer *gfx.Renderer
	post     func(func())

	have    map[thumbKey]*thumbTex
	failed  map[thumbKey]bool
	working bool
	draws   int
	free    []draw.ImageID
	next    draw.ImageID
	made    int // pictures made, for the log
	// wanted is the recordings asked for last, in order.
	wanted []library.Recording
}

func newThumbs(device *sdl.GPUDevice, r *gfx.Renderer, post func(func())) *thumbs {
	return &thumbs{device: device, renderer: r, post: post, have: map[thumbKey]*thumbTex{}, failed: map[thumbKey]bool{}, next: thumbBase}
}

// id is r's picture, 0 while there's none.
func (t *thumbs) id(r library.Recording) draw.ImageID {
	if t == nil {
		return 0
	}
	if x, ok := t.have[keyOf(r)]; ok {
		return x.id
	}
	return 0
}

// want asks for the pictures of recs, the visible ones (paths) first, and
// marks those drawn.
func (t *thumbs) want(recs []library.Recording, visible []string) {
	t.draws++
	var first, rest []library.Recording
	for _, r := range recs {
		if slices.Contains(visible, r.Path) {
			first = append(first, r)
			if x, ok := t.have[keyOf(r)]; ok {
				x.used = t.draws
			}
		} else {
			rest = append(rest, r)
		}
	}
	t.wanted = append(first, rest...)
	if len(t.wanted) > maxThumbs {
		t.wanted = t.wanted[:maxThumbs]
	}
	t.start()
}

// start makes the next picture wanted, unless one is being made.
func (t *thumbs) start() {
	if t.working {
		return
	}
	for _, r := range t.wanted {
		k := keyOf(r)
		if _, ok := t.have[k]; ok || t.failed[k] {
			continue
		}
		t.working = true
		go func() {
			img, err := makeThumb(r.Path, r.Duration)
			t.post(func() { t.got(k, img, err) })
		}()
		return
	}
}

// got takes a picture that was made.
func (t *thumbs) got(k thumbKey, img *image.RGBA, err error) {
	t.working = false
	if err != nil {
		slog.Warn("app: a recording's picture", "path", k.path, "err", err)
		t.failed[k] = true
		t.start()
		return
	}
	tex, err := gfx.UploadTexture(t.device, sdl.GPU_TEXTUREFORMAT_R8G8B8A8_UNORM, thumbW, thumbH, img.Pix)
	if err != nil {
		slog.Warn("app: upload a recording's picture", "err", err)
		t.failed[k] = true
		t.start()
		return
	}
	if len(t.have) >= maxThumbs {
		t.evict()
	}
	var id draw.ImageID
	if n := len(t.free); n > 0 {
		id, t.free = t.free[n-1], t.free[:n-1]
	} else {
		id, t.next = t.next, t.next+1
	}
	t.renderer.SetImage(id, tex)
	if t.made == 0 {
		slog.Info("app: first recording's picture", "path", k.path)
	}
	t.made++
	t.have[k] = &thumbTex{id: id, tex: tex, used: t.draws}
	t.start()
}

// evict lets go of the picture drawn longest ago.
func (t *thumbs) evict() {
	var old thumbKey
	oldest := math.MaxInt
	for k, x := range t.have {
		if x.used < oldest {
			old, oldest = k, x.used
		}
	}
	if x, ok := t.have[old]; ok {
		t.renderer.SetImage(x.id, nil)
		t.device.ReleaseTexture(x.tex)
		t.free = append(t.free, x.id)
		delete(t.have, old)
	}
}

// close lets go of every picture.
func (t *thumbs) close() {
	for len(t.have) > 0 {
		t.evict()
	}
}

// makeThumb is a card's picture of the recording at path: the frame a
// third of the way in (at most 2 s), made large enough to cover
// thumbW×thumbH and cut to it. Any goroutine.
func makeThumb(path string, d time.Duration) (*image.RGBA, error) {
	f, err := mp4.ReadFile(path)
	if err != nil {
		return nil, err
	}
	v, _ := f.Track("vide")
	edge := thumbW
	if v.Width > 0 && v.Height > 0 {
		s := max(thumbW/v.Width, thumbH/v.Height)
		edge = int(math.Ceil(max(v.Width, v.Height) * s))
	}
	fr, err := mac.OpenFrames(path)
	if err != nil {
		return nil, err
	}
	defer fr.Close()
	img, _, err := fr.At(min(d/3, 2*time.Second), edge)
	if err != nil {
		return nil, err
	}
	return thumb.Cover(img, thumbW, thumbH), nil
}

// liveThumbs gives the recordings in a render of this Mac (render -live)
// their pictures, and returns what lets them go.
func liveThumbs(o *offscreen, v *home.View) func() {
	var texs []*sdl.GPUTexture
	for i, r := range v.St.Recordings {
		img, err := makeThumb(r.Path, r.Duration)
		if err != nil {
			slog.Warn("render: a recording's picture", "path", r.Path, "err", err)
			continue
		}
		tex, err := gfx.UploadTexture(o.device, sdl.GPU_TEXTUREFORMAT_R8G8B8A8_UNORM, thumbW, thumbH, img.Pix)
		if err != nil {
			slog.Warn("render: upload a recording's picture", "err", err)
			continue
		}
		id := thumbBase + draw.ImageID(i)
		o.renderer.SetImage(id, tex)
		v.St.Recordings[i].Thumb = id
		texs = append(texs, tex)
	}
	return func() {
		for _, tex := range texs {
			o.device.ReleaseTexture(tex)
		}
	}
}
