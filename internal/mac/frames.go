package mac

// The pictures of a recording, one at a time: where a trim starts and
// ends. AVAssetImageGenerator decodes from the keyframe before the time
// asked for, with no tolerance, so the picture is exactly the frame shown
// then.

import (
	"errors"
	"fmt"
	"image"
	"os"
	"sync"
	"time"

	"github.com/ebitengine/purego/objc"
)

// cgSize is CoreGraphics' CGSize.
type cgSize struct{ W, H float64 }

// Frames reads the frames of one recording. Its methods may be called
// from any goroutine, one at a time.
type Frames struct {
	mu  sync.Mutex
	gen objc.ID // AVAssetImageGenerator, owned; 0 when closed
}

// OpenFrames opens the recording at path for reading frames.
func OpenFrames(path string) (*Frames, error) {
	if err := load(); err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("frames: %w", err)
	}
	f := &Frames{}
	withPool(func() {
		asset := class("AVURLAsset").Send(sel("URLAssetWithURL:options:"), fileURL(path), objc.ID(0))
		if asset == 0 {
			return
		}
		gen := class("AVAssetImageGenerator").Send(sel("assetImageGeneratorWithAsset:"), asset)
		if gen == 0 {
			return
		}
		gen.Send(sel("setAppliesPreferredTrackTransform:"), true)
		gen.Send(sel("setRequestedTimeToleranceBefore:"), TimeOf(0, 1))
		gen.Send(sel("setRequestedTimeToleranceAfter:"), TimeOf(0, 1))
		f.gen = gen.Send(sel("retain"))
	})
	if f.gen == 0 {
		return nil, fmt.Errorf("frames: can't open %s", path)
	}
	return f, nil
}

// At is the frame shown at t, scaled down so its long edge is at most
// longEdge pixels (0: full size), and the time of the frame it is.
func (f *Frames) At(t time.Duration, longEdge int) (*image.RGBA, time.Duration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gen == 0 {
		return nil, 0, errors.New("frames: closed")
	}
	type result struct {
		img *image.RGBA
		at  time.Duration
		err error
	}
	var r result
	withPool(func() {
		f.gen.Send(sel("setMaximumSize:"), cgSize{float64(longEdge), float64(longEdge)})
		got := make(chan result, 1)
		// The handler's CMTime is a 24-byte struct, which arm64 passes as
		// a pointer to a copy (and purego's blocks take no structs).
		done := objc.NewBlock(func(_ objc.Block, img, actual uintptr, nserr objc.ID) {
			if img == 0 {
				got <- result{err: nsError("frames", nserr)}
				return
			}
			var at time.Duration
			if actual != 0 {
				at = time.Duration((*CMTime)(ptr(actual)).Nanos())
			}
			rgba, err := copyImage(img)
			got <- result{rgba, at, err}
		})
		defer done.Release()
		f.gen.Send(sel("generateCGImageAsynchronouslyForTime:completionHandler:"), Nanos(int64(t)), done)
		select {
		case r = <-got:
		case <-time.After(callTimeout):
			r.err = fmt.Errorf("frames: %w", errTimeout)
		}
	})
	return r.img, r.at, r.err
}

// Close stops any frame being made and lets the recording go.
func (f *Frames) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gen == 0 {
		return
	}
	withPool(func() {
		f.gen.Send(sel("cancelAllCGImageGeneration"))
		f.gen.Send(sel("release"))
	})
	f.gen = 0
}
