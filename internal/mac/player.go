package mac

// Playing a recording: AVPlayer plays its sound and keeps its clock, and
// an AVPlayerItemVideoOutput hands over the frame for a host time, which
// the app takes at each of its window's frames, so the picture follows
// the sound. Frames come as BGRA, scaled by the output to the size asked
// for (a recording is up to 4096×2304; the window shows less).
//
// AVPlayerItem raises an exception for a seek with a completion handler
// before it's ready to play, and for a negative tolerance: Player seeks
// without a handler, with zero or positive tolerances, and only once the
// item is ready (a seek asked for before then waits for it).

import (
	"errors"
	"fmt"
	"os"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// AVPlayerItemStatus values (AVPlayerItem.h).
const (
	avItemReady  = 1
	avItemFailed = 2
)

// AVPlayerTimeControlStatus values (AVPlayer.h).
const avPlaying = 2

// Player plays one recording. Its methods run on one goroutine (the app's
// main thread), never two at once.
type Player struct {
	player objc.ID // AVPlayer, owned; 0 when closed
	item   objc.ID // AVPlayerItem, owned
	output objc.ID // AVPlayerItemVideoOutput, owned; 0 until the size is known
	outW   int     // the frames' size asked of output
	outH   int
	ready  bool
	seek   *seekTo // asked for before ready
}

type seekTo struct {
	t     time.Duration
	exact bool
}

// The messages Player sends at every frame, registered once (objc.Send
// registers a function on every call).
var play struct {
	itemTimeSel, hasNewSel, copySel, currentSel objc.SEL

	itemTime func(id objc.ID, sel objc.SEL, host float64) CMTime
	hasNew   func(id objc.ID, sel objc.SEL, t CMTime) bool
	copyPB   func(id objc.ID, sel objc.SEL, t CMTime, shown *CMTime) uintptr
	current  func(id objc.ID, sel objc.SEL) CMTime
}

// loadPlayer resolves what Player calls at every frame (inside loadOnce).
func loadPlayer() error {
	libobjc, err := purego.Dlopen("/usr/lib/libobjc.A.dylib", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return err
	}
	purego.RegisterLibFunc(&play.itemTime, libobjc, "objc_msgSend")
	purego.RegisterLibFunc(&play.hasNew, libobjc, "objc_msgSend")
	purego.RegisterLibFunc(&play.copyPB, libobjc, "objc_msgSend")
	purego.RegisterLibFunc(&play.current, libobjc, "objc_msgSend")
	play.itemTimeSel, play.hasNewSel = sel("itemTimeForHostTime:"), sel("hasNewPixelBufferForItemTime:")
	play.copySel, play.currentSel = sel("copyPixelBufferForItemTime:itemTimeForDisplay:"), sel("currentTime")
	return nil
}

// OpenPlayer opens the recording at path, paused at its start. Nothing
// shows until SetSize says how big the frames should be.
func OpenPlayer(path string) (*Player, error) {
	if err := load(); err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("player: %w", err)
	}
	p := &Player{}
	withPool(func() {
		item := class("AVPlayerItem").Send(sel("playerItemWithURL:"), fileURL(path))
		if item == 0 {
			return
		}
		player := class("AVPlayer").Send(sel("playerWithPlayerItem:"), item)
		if player == 0 {
			return
		}
		p.item, p.player = item.Send(sel("retain")), player.Send(sel("retain"))
	})
	if p.player == 0 {
		return nil, fmt.Errorf("player: can't open %s", path)
	}
	return p, nil
}

// SetSize asks for frames w×h pixels (the picture as drawn), BGRA. The
// output is made again for a new size: the next frame comes at it.
func (p *Player) SetSize(w, h int) {
	if p.player == 0 || w <= 0 || h <= 0 || (w == p.outW && h == p.outH) {
		return
	}
	withPool(func() {
		if p.output != 0 {
			p.item.Send(sel("removeOutput:"), p.output)
			p.output.Send(sel("release"))
		}
		attrs := nsDict(
			constant("kCVPixelBufferPixelFormatTypeKey"), nsInt(pixelBGRA),
			constant("kCVPixelBufferWidthKey"), nsInt(w),
			constant("kCVPixelBufferHeightKey"), nsInt(h),
		)
		p.output = class("AVPlayerItemVideoOutput").Send(sel("alloc")).Send(sel("initWithPixelBufferAttributes:"), attrs)
		p.item.Send(sel("addOutput:"), p.output)
	})
	p.outW, p.outH = w, h
}

// Ready reports whether the recording can play yet, and the error that
// keeps it from playing.
func (p *Player) Ready() (bool, error) {
	if p.player == 0 {
		return false, errors.New("player: closed")
	}
	if p.ready {
		return true, nil
	}
	var err error
	withPool(func() {
		switch objc.Send[int](p.item, sel("status")) {
		case avItemReady:
			p.ready = true
		case avItemFailed:
			err = nsError("player", p.item.Send(sel("error")))
		}
	})
	if p.ready && p.seek != nil {
		s := p.seek
		p.seek = nil
		p.Seek(s.t, s.exact)
	}
	return p.ready, err
}

// Duration is how long the recording plays (0 until it's ready).
func (p *Player) Duration() time.Duration {
	if !p.ready {
		return 0
	}
	return time.Duration(objc.Send[CMTime](p.item, sel("duration")).Nanos())
}

// Play plays from where it is.
func (p *Player) Play() {
	if p.player != 0 {
		withPool(func() { p.player.Send(sel("play")) })
	}
}

// Pause holds it where it is.
func (p *Player) Pause() {
	if p.player != 0 {
		withPool(func() { p.player.Send(sel("pause")) })
	}
}

// Playing reports whether it's playing, or waiting to (after Play, until
// Pause or the end).
func (p *Player) Playing() bool {
	return p.player != 0 && objc.Send[float32](p.player, sel("rate")) != 0
}

// Waiting reports whether it was told to play but isn't yet (buffering
// after a seek).
func (p *Player) Waiting() bool {
	return p.Playing() && objc.Send[int](p.player, sel("timeControlStatus")) != avPlaying
}

// Time is where it's playing.
func (p *Player) Time() time.Duration {
	if p.player == 0 {
		return 0
	}
	return time.Duration(play.current(p.player, play.currentSel).Nanos())
}

// Seek moves to t: exactly (the frame shown at t), or to a frame near it
// that's quick to decode (while dragging).
func (p *Player) Seek(t time.Duration, exact bool) {
	if p.player == 0 {
		return
	}
	if !p.ready {
		p.seek = &seekTo{t, exact}
		return
	}
	tol := Nanos(0)
	if !exact {
		tol = Nanos(int64(100 * time.Millisecond))
	}
	withPool(func() {
		p.player.Send(sel("seekToTime:toleranceBefore:toleranceAfter:"), Nanos(int64(max(t, 0))), tol, tol)
	})
}

// SetMuted turns its sound off or on.
func (p *Player) SetMuted(muted bool) {
	if p.player != 0 {
		withPool(func() { p.player.Send(sel("setMuted:"), muted) })
	}
}

// Frame calls fn with the frame to show at host (seconds on the host
// clock) when it's one fn hasn't been given yet, and returns its time;
// ok is false, and fn isn't called, when there's none new. The pixels are
// BGRA, w×h, stride bytes a row, valid only during fn.
func (p *Player) Frame(host float64, fn func(pix []byte, w, h, stride int)) (at time.Duration, ok bool) {
	if p.output == 0 || !p.ready {
		return 0, false
	}
	t := play.itemTime(p.output, play.itemTimeSel, host)
	if !t.Valid() || !play.hasNew(p.output, play.hasNewSel, t) {
		return 0, false
	}
	var shown CMTime
	pb := play.copyPB(p.output, play.copySel, t, &shown)
	if pb == 0 {
		return 0, false
	}
	defer lib.cfRelease(pb)
	if !pixels(pb, fn) {
		return 0, false
	}
	if !shown.Valid() {
		shown = t
	}
	return time.Duration(shown.Nanos()), true
}

// pixels calls fn with a BGRA pixel buffer's pixels, locked for reading.
func pixels(pb uintptr, fn func(pix []byte, w, h, stride int)) bool {
	if cm.pixelFormat(pb) != pixelBGRA || cm.isPlanar(pb) {
		return false
	}
	if cm.lockBase(pb, pixelLockReadOnly) != 0 {
		return false
	}
	defer cm.unlock(pb, pixelLockReadOnly)
	w, h, stride := int(cm.pixelWidth(pb)), int(cm.height(pb)), int(cm.bytesPerRow(pb))
	base := cm.baseAddress(pb)
	if base == nil || w <= 0 || h <= 0 || stride < w*4 {
		return false
	}
	fn(unsafe.Slice((*byte)(base), (h-1)*stride+w*4), w, h, stride)
	return true
}

// Close stops it and lets the recording go.
func (p *Player) Close() {
	if p.player == 0 {
		return
	}
	withPool(func() {
		p.player.Send(sel("pause"))
		if p.output != 0 {
			p.item.Send(sel("removeOutput:"), p.output)
			p.output.Send(sel("release"))
		}
		p.player.Send(sel("release"))
		p.item.Send(sel("release"))
	})
	p.player, p.item, p.output = 0, 0, 0
}
